// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/barahn/remotekit/webrtc"
)

func TestEncodeSignal_Signed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()

	data, err := encodeSignal(webrtc.SignalMessage{Type: webrtc.SignalAnswer, SessionID: "agent-1", SDP: "v=0"}, "viewer-a", priv, now)
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["viewer_id"] != "viewer-a" {
		t.Errorf("viewer_id = %v, want viewer-a (the server routes on it)", fields["viewer_id"])
	}

	msg, err := webrtc.DecodeSignalMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := msg.VerifyFrom(pub, now); err != nil {
		t.Fatalf("signed answer does not verify against the device key: %v", err)
	}
	if msg.TargetID != "viewer-a" {
		t.Errorf("TargetID = %q, want the viewer, so the signature binds it", msg.TargetID)
	}

	// Redirected to another viewer, the signature no longer holds.
	msg.TargetID = "viewer-b"
	if err := msg.VerifyFrom(pub, now); !errors.Is(err, webrtc.ErrSignalBadSig) {
		t.Errorf("retargeted message: want ErrSignalBadSig, got %v", err)
	}

	// Nor does it with a tampered SDP.
	msg.TargetID = "viewer-a"
	msg.SDP = "v=0\r\na=evil"
	if err := msg.VerifyFrom(pub, now); !errors.Is(err, webrtc.ErrSignalBadSig) {
		t.Errorf("tampered SDP: want ErrSignalBadSig, got %v", err)
	}
}

func TestEncodeSignal_UnsignedWithoutKey(t *testing.T) {
	data, err := encodeSignal(webrtc.SignalMessage{Type: webrtc.SignalCandidate, SessionID: "agent-1", Candidate: "{}"}, "viewer-a", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sig", "pub_key", "nonce", "target_id"} {
		if _, ok := fields[k]; ok {
			t.Errorf("keyless agent's message carries %q; it should go out as before", k)
		}
	}
	if fields["type"] != "candidate" || fields["session_id"] != "agent-1" || fields["viewer_id"] != "viewer-a" || fields["candidate"] != "{}" {
		t.Errorf("unexpected message: %s", data)
	}
}

func TestSignalSigningKey(t *testing.T) {
	if k, err := signalSigningKey(&AgentCredentials{AgentID: "a"}); k != nil || err != nil {
		t.Errorf("keyless agent: got key=%v err=%v, want nil, nil", k != nil, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")
	want, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signalSigningKey(&AgentCredentials{AgentID: "a", DeviceKeyPath: path})
	if err != nil || !got.Equal(want) {
		t.Errorf("keyed agent: got err=%v, same key=%v", err, got.Equal(want))
	}

	if _, err := signalSigningKey(&AgentCredentials{AgentID: "a", DeviceKeyPath: filepath.Join(dir, "missing.key")}); err == nil {
		t.Error("missing key file: want an error rather than signing nothing")
	}
}
