// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadOrCreateDeviceKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "device.key")

	first, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("key file mode = %v, want 0600", perm)
		}
	}

	// Loading again must return the same identity, not a fresh one.
	second, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !first.Equal(second) {
		t.Fatal("reloading the device key produced a different key")
	}
	if _, err := LoadDeviceKey(path); err != nil {
		t.Fatalf("LoadDeviceKey: %v", err)
	}
}

func TestLoadDeviceKey_RefusesReadableByOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not describe Windows ACLs")
	}
	path := filepath.Join(t.TempDir(), "device.key")
	if _, err := LoadOrCreateDeviceKey(path); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadDeviceKey(path); !errors.Is(err, ErrDeviceKeyPermissions) {
		t.Fatalf("LoadDeviceKey: want ErrDeviceKeyPermissions, got %v", err)
	}
	if _, err := LoadOrCreateDeviceKey(path); !errors.Is(err, ErrDeviceKeyPermissions) {
		t.Fatalf("LoadOrCreateDeviceKey: want ErrDeviceKeyPermissions, got %v", err)
	}
}

func TestLoadDeviceKey_RejectsGarbageAndMissing(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.key")
	if err := os.WriteFile(garbage, []byte("not a key"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadDeviceKey(garbage); !errors.Is(err, ErrDeviceKeyMalformed) {
		t.Fatalf("garbage: want ErrDeviceKeyMalformed, got %v", err)
	}
	// Connecting must never mint a new identity.
	if _, err := LoadDeviceKey(filepath.Join(dir, "absent.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: want os.ErrNotExist, got %v", err)
	}
}

func TestParseDevicePublicKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	got, ok := ParseDevicePublicKey(EncodeDevicePublicKey(pub))
	if !ok || !got.Equal(pub) {
		t.Fatal("round trip of a real key failed")
	}
	for _, s := range []string{"", "pubkey-123", "c2hvcnQ=", "not base64!"} {
		if _, ok := ParseDevicePublicKey(s); ok {
			t.Errorf("%q parsed as a device key", s)
		}
	}
}

func TestConnectProof(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_790_000_000, 0)
	ts, sig := SignConnect(priv, "agent-1", now)

	if err := VerifyConnect(pub, "agent-1", ts, sig, now.Add(30*time.Second)); err != nil {
		t.Fatalf("valid proof: %v", err)
	}
	cases := []struct {
		name string
		err  error
		got  error
	}{
		{"other agent", ErrConnectSigInvalid, VerifyConnect(pub, "agent-2", ts, sig, now)},
		{"other key", ErrConnectSigInvalid, VerifyConnect(otherPub, "agent-1", ts, sig, now)},
		{"altered timestamp", ErrConnectSigInvalid, VerifyConnect(pub, "agent-1", "1790000001", sig, now)},
		{"too old", ErrConnectSigStale, VerifyConnect(pub, "agent-1", ts, sig, now.Add(ConnectSignatureSkew+time.Second))},
		{"too far ahead", ErrConnectSigStale, VerifyConnect(pub, "agent-1", ts, sig, now.Add(-ConnectSignatureSkew-time.Second))},
		{"no signature", ErrConnectSigMissing, VerifyConnect(pub, "agent-1", ts, "", now)},
		{"no timestamp", ErrConnectSigMissing, VerifyConnect(pub, "agent-1", "", sig, now)},
		{"garbage signature", ErrConnectSigInvalid, VerifyConnect(pub, "agent-1", ts, "!!", now)},
	}
	for _, c := range cases {
		if !errors.Is(c.got, c.err) {
			t.Errorf("%s: want %v, got %v", c.name, c.err, c.got)
		}
	}
}

func proofRequest(t *testing.T, priv ed25519.PrivateKey, agentID string, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/tunnel/connect", nil)
	if priv != nil {
		ts, sig := SignConnect(priv, agentID, now)
		r.Header.Set(headerAgentTimestamp, ts)
		r.Header.Set(headerAgentSignature, sig)
	}
	return r
}

func TestCheckDeviceProof(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	keyed := AgentIdentity{ID: "agent-1", PublicKey: EncodeDevicePublicKey(pub)}
	legacy := AgentIdentity{ID: "agent-2", PublicKey: "pubkey-123"}
	now := time.Now()

	lenient := &TunnelServer{}
	strict := &TunnelServer{RequireDeviceProof: true}

	for _, ts := range []*TunnelServer{lenient, strict} {
		if err := ts.checkDeviceProof(keyed, proofRequest(t, priv, "agent-1", now)); err != nil {
			t.Errorf("strict=%v: valid proof refused: %v", ts.RequireDeviceProof, err)
		}
		// A proof that is present but wrong is refused whatever the mode.
		if err := ts.checkDeviceProof(keyed, proofRequest(t, strangerPriv, "agent-1", now)); !errors.Is(err, ErrConnectSigInvalid) {
			t.Errorf("strict=%v: stranger's proof: want ErrConnectSigInvalid, got %v", ts.RequireDeviceProof, err)
		}
		// Agents without a device key are never held to a proof.
		if err := ts.checkDeviceProof(legacy, proofRequest(t, nil, "", now)); err != nil {
			t.Errorf("strict=%v: legacy agent refused: %v", ts.RequireDeviceProof, err)
		}
	}

	// The two modes differ only on a keyed agent that sends nothing.
	if err := lenient.checkDeviceProof(keyed, proofRequest(t, nil, "", now)); err != nil {
		t.Errorf("lenient: missing proof should be tolerated, got %v", err)
	}
	if err := strict.checkDeviceProof(keyed, proofRequest(t, nil, "", now)); !errors.Is(err, ErrConnectSigMissing) {
		t.Errorf("strict: missing proof: want ErrConnectSigMissing, got %v", err)
	}
}

// End to end: an agent enrolled with a device key connects to a server that
// requires the proof, and one whose key file has gone missing is refused
// before it ever dials.
func TestEnrollWithDeviceKey_ConnectsUnderRequiredProof(t *testing.T) {
	store := NewMemStore()
	store.SeedPairingCode(HashCredential("DEVKEY01"), PairingCode{ID: "pc-dk", ExpiresAt: time.Now().Add(10 * time.Minute)})
	ts := NewTunnelServer(store)
	ts.RequireDeviceProof = true
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/pair", ts.HandlePairing)
	mux.HandleFunc("/tunnel/connect", ts.HandleConnect)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "device.key")
	savePath := filepath.Join(dir, "agent.pem")
	creds, err := EnrollWithDeviceKey(srv.URL, "DEVKEY01", "h", "linux", "amd64", keyPath, savePath, false)
	if err != nil {
		t.Fatalf("EnrollWithDeviceKey: %v", err)
	}
	saved, err := LoadCredentials(savePath)
	if err != nil || saved.DeviceKeyPath != keyPath {
		t.Fatalf("saved DeviceKeyPath = %q (err %v), want %q", saved.DeviceKeyPath, err, keyPath)
	}
	stored, err := store.GetAgentByID(context.Background(), creds.AgentID)
	if err != nil {
		t.Fatalf("GetAgentByID: %v", err)
	}
	if _, ok := ParseDevicePublicKey(stored.PublicKey); !ok {
		t.Fatalf("server stored %q, not a device key", stored.PublicKey)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = NewTunnelClient(savePath, false).Connect(ctx, creds) }()
	deadline := time.Now().Add(2 * time.Second)
	for !ts.IsSessionConnected(creds.AgentID) {
		if time.Now().After(deadline) {
			t.Fatal("agent with a valid device proof never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}

	missing := *creds
	missing.DeviceKeyPath = filepath.Join(dir, "gone.key")
	if err := NewTunnelClient(savePath, false).Connect(context.Background(), &missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key file: want os.ErrNotExist before dialling, got %v", err)
	}
}
