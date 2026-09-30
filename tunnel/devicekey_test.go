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
	"strconv"
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
	p := SignConnect(priv, AudienceTunnel, "agent-1", now)

	if err := VerifyConnect(pub, AudienceTunnel, "agent-1", p, now.Add(30*time.Second), &ReplayCache{}); err != nil {
		t.Fatalf("valid proof: %v", err)
	}
	with := func(f func(*ConnectProof)) ConnectProof {
		q := p
		f(&q)
		return q
	}
	verify := func(pub ed25519.PublicKey, audience, agentID string, p ConnectProof, now time.Time) error {
		return VerifyConnect(pub, audience, agentID, p, now, &ReplayCache{})
	}
	cases := []struct {
		name string
		err  error
		got  error
	}{
		{"other agent", ErrConnectSigInvalid, verify(pub, AudienceTunnel, "agent-2", p, now)},
		{"other key", ErrConnectSigInvalid, verify(otherPub, AudienceTunnel, "agent-1", p, now)},
		{"other audience", ErrConnectSigInvalid, verify(pub, AudienceSignal, "agent-1", p, now)},
		{"altered timestamp", ErrConnectSigInvalid, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Timestamp = "1790000001" }), now)},
		{"altered nonce", ErrConnectSigInvalid, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Nonce = "AAAAAAAAAAAAAAAAAAAAAAAAAA" }), now)},
		{"malformed nonce", ErrConnectSigInvalid, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Nonce = "short" }), now)},
		{"too old", ErrConnectSigStale, verify(pub, AudienceTunnel, "agent-1", p, now.Add(ConnectSignatureSkew+time.Second))},
		{"too far ahead", ErrConnectSigStale, verify(pub, AudienceTunnel, "agent-1", p, now.Add(-ConnectSignatureSkew-time.Second))},
		{"no signature", ErrConnectSigMissing, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Signature = "" }), now)},
		{"no timestamp", ErrConnectSigMissing, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Timestamp = "" }), now)},
		{"no nonce", ErrConnectSigMissing, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Nonce = "" }), now)},
		{"garbage signature", ErrConnectSigInvalid, verify(pub, AudienceTunnel, "agent-1", with(func(q *ConnectProof) { q.Signature = "!!" }), now)},
	}
	for _, c := range cases {
		if !errors.Is(c.got, c.err) {
			t.Errorf("%s: want %v, got %v", c.name, c.err, c.got)
		}
	}

	if q := SignConnect(priv, AudienceTunnel, "agent-1", now); q.Nonce == p.Nonce {
		t.Error("two proofs share a nonce")
	}
}

func TestConnectProof_Replay(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_790_000_000, 0)
	p := SignConnect(priv, AudienceTunnel, "agent-1", now)
	var seen ReplayCache

	if err := VerifyConnect(pub, AudienceTunnel, "agent-1", p, now, &seen); err != nil {
		t.Fatalf("first use: %v", err)
	}
	// Anywhere inside the window, the same proof is refused a second time.
	for _, at := range []time.Duration{0, time.Second, ConnectSignatureSkew} {
		if err := VerifyConnect(pub, AudienceTunnel, "agent-1", p, now.Add(at), &seen); !errors.Is(err, ErrConnectSigReplayed) {
			t.Errorf("replay at +%v: want ErrConnectSigReplayed, got %v", at, err)
		}
	}
	// A fresh proof from the same agent is still accepted.
	if err := VerifyConnect(pub, AudienceTunnel, "agent-1", SignConnect(priv, AudienceTunnel, "agent-1", now), now, &seen); err != nil {
		t.Errorf("fresh proof after a replay: %v", err)
	}
	// A proof that fails verification is not recorded: its nonce stays usable.
	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	forged := SignConnect(strangerPriv, AudienceTunnel, "agent-1", now)
	if err := VerifyConnect(pub, AudienceTunnel, "agent-1", forged, now, &seen); !errors.Is(err, ErrConnectSigInvalid) {
		t.Fatalf("forged proof: want ErrConnectSigInvalid, got %v", err)
	}
	if _, ok := seen.seen["agent-1\x00"+forged.Nonce]; ok {
		t.Error("a forged proof's nonce was recorded")
	}
}

func TestReplayCache_Sweeps(t *testing.T) {
	var c ReplayCache
	now := time.Unix(1_790_000_000, 0)
	for i := range 100 {
		c.use(string(rune('a'+i%26))+strconv.Itoa(i), now.Add(ConnectSignatureSkew), now)
	}
	later := now.Add(3 * ConnectSignatureSkew)
	if !c.use("new", later.Add(ConnectSignatureSkew), later) {
		t.Fatal("new key refused")
	}
	if len(c.seen) != 1 {
		t.Errorf("expired entries kept: %d left, want 1", len(c.seen))
	}
	// An expired entry is also no bar to reuse, sweep or not.
	if !c.use("new", later.Add(3*ConnectSignatureSkew), later.Add(2*ConnectSignatureSkew)) {
		t.Error("key refused after its expiry")
	}
}

func proofRequest(t *testing.T, priv ed25519.PrivateKey, audience, agentID string, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/tunnel/connect", nil)
	if priv != nil {
		SignConnect(priv, audience, agentID, now).setHeader(r.Header)
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
		if err := ts.checkDeviceProof(keyed, proofRequest(t, priv, AudienceTunnel, "agent-1", now)); err != nil {
			t.Errorf("strict=%v: valid proof refused: %v", ts.RequireDeviceProof, err)
		}
		// A proof that is present but wrong is refused whatever the mode.
		if err := ts.checkDeviceProof(keyed, proofRequest(t, strangerPriv, AudienceTunnel, "agent-1", now)); !errors.Is(err, ErrConnectSigInvalid) {
			t.Errorf("strict=%v: stranger's proof: want ErrConnectSigInvalid, got %v", ts.RequireDeviceProof, err)
		}
		// Agents without a device key are never held to a proof.
		if err := ts.checkDeviceProof(legacy, proofRequest(t, nil, "", "", now)); err != nil {
			t.Errorf("strict=%v: legacy agent refused: %v", ts.RequireDeviceProof, err)
		}
	}

	// A proof made for the signalling socket is refused by the tunnel, and a
	// proof already accepted is refused the second time, whatever the mode.
	for _, ts := range []*TunnelServer{lenient, strict} {
		if err := ts.checkDeviceProof(keyed, proofRequest(t, priv, AudienceSignal, "agent-1", now)); !errors.Is(err, ErrConnectSigInvalid) {
			t.Errorf("strict=%v: signalling proof on the tunnel: want ErrConnectSigInvalid, got %v", ts.RequireDeviceProof, err)
		}
		r := proofRequest(t, priv, AudienceTunnel, "agent-1", now)
		if err := ts.checkDeviceProof(keyed, r); err != nil {
			t.Fatalf("strict=%v: valid proof refused: %v", ts.RequireDeviceProof, err)
		}
		if err := ts.checkDeviceProof(keyed, r); !errors.Is(err, ErrConnectSigReplayed) {
			t.Errorf("strict=%v: replayed proof: want ErrConnectSigReplayed, got %v", ts.RequireDeviceProof, err)
		}
	}

	// The two modes differ only on a keyed agent that sends nothing.
	if err := lenient.checkDeviceProof(keyed, proofRequest(t, nil, "", "", now)); err != nil {
		t.Errorf("lenient: missing proof should be tolerated, got %v", err)
	}
	if err := strict.checkDeviceProof(keyed, proofRequest(t, nil, "", "", now)); !errors.Is(err, ErrConnectSigMissing) {
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
