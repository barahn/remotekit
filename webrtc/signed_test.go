// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func signedOffer(t *testing.T, priv ed25519.PrivateKey, now time.Time) *SignalMessage {
	t.Helper()
	m := &SignalMessage{Type: SignalOffer, SessionID: "sess-1", TargetID: "agent-1", SDP: "v=0\r\n"}
	if err := m.Sign(priv, now, time.Minute); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return m
}

func TestSignedSignal_RoundTripThroughJSON(t *testing.T) {
	pub, priv := newKey(t)
	now := time.Unix(1_790_000_000, 0)
	m := signedOffer(t, priv, now)

	// What matters is that the signature survives the wire, not just memory.
	data, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeSignalMessage(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := got.VerifyFrom(pub, now.Add(10*time.Second)); err != nil {
		t.Fatalf("VerifyFrom after round trip: %v", err)
	}
}

func TestSignedSignal_AnyTamperingIsRejected(t *testing.T) {
	_, priv := newKey(t)
	now := time.Unix(1_790_000_000, 0)

	tamper := map[string]func(m *SignalMessage){
		"type":       func(m *SignalMessage) { m.Type = SignalClose },
		"session":    func(m *SignalMessage) { m.SessionID = "sess-2" },
		"target":     func(m *SignalMessage) { m.TargetID = "agent-2" },
		"sdp":        func(m *SignalMessage) { m.SDP = "v=1\r\n" },
		"candidate":  func(m *SignalMessage) { m.Candidate = "x" },
		"error":      func(m *SignalMessage) { m.Error = "x" },
		"nonce":      func(m *SignalMessage) { m.Nonce = "other" },
		"issued_at":  func(m *SignalMessage) { m.IssuedAt-- },
		"expires_at": func(m *SignalMessage) { m.ExpiresAt++ },
	}
	for name, mutate := range tamper {
		t.Run(name, func(t *testing.T) {
			m := signedOffer(t, priv, now)
			mutate(m)
			if err := m.Verify(now); !errors.Is(err, ErrSignalBadSig) {
				t.Fatalf("want ErrSignalBadSig, got %v", err)
			}
		})
	}
}

// Swapping in another key must not let the attacker keep the old signature,
// and re-signing with their own key must be caught by VerifyFrom.
func TestSignedSignal_SignerIsBound(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, otherPriv := newKey(t)
	now := time.Unix(1_790_000_000, 0)

	m := signedOffer(t, priv, now)
	m.PubKey = base64.StdEncoding.EncodeToString(otherPub)
	if err := m.Verify(now); !errors.Is(err, ErrSignalBadSig) {
		t.Fatalf("re-attributed signature: want ErrSignalBadSig, got %v", err)
	}

	forged := signedOffer(t, otherPriv, now)
	if err := forged.Verify(now); err != nil {
		t.Fatalf("self-consistent message should pass Verify: %v", err)
	}
	if err := forged.VerifyFrom(pub, now); !errors.Is(err, ErrSignalWrongSigner) {
		t.Fatalf("stranger's key: want ErrSignalWrongSigner, got %v", err)
	}
}

func TestSignedSignal_ValidityWindow(t *testing.T) {
	_, priv := newKey(t)
	now := time.Unix(1_790_000_000, 0)
	m := signedOffer(t, priv, now) // valid [now, now+1m)

	cases := []struct {
		at   time.Time
		want error
	}{
		{now, nil},
		{now.Add(59 * time.Second), nil},
		{now.Add(time.Minute), ErrSignalExpired},
		{now.Add(-SignalClockSkew), nil},
		{now.Add(-SignalClockSkew - time.Second), ErrSignalNotYetValid},
	}
	for _, c := range cases {
		if err := m.Verify(c.at); !errors.Is(err, c.want) {
			t.Errorf("Verify at %+ds: want %v, got %v", c.at.Unix()-now.Unix(), c.want, err)
		}
	}
}

func TestSignedSignal_LifetimeIsBounded(t *testing.T) {
	_, priv := newKey(t)
	now := time.Unix(1_790_000_000, 0)
	m := &SignalMessage{Type: SignalOffer}
	if err := m.Sign(priv, now, MaxSignalLifetime+time.Second); !errors.Is(err, ErrSignalLifetime) {
		t.Fatalf("Sign with overlong ttl: want ErrSignalLifetime, got %v", err)
	}
	if err := m.Sign(priv, now, 0); !errors.Is(err, ErrSignalLifetime) {
		t.Fatalf("Sign with zero ttl: want ErrSignalLifetime, got %v", err)
	}
}

func TestSignedSignal_UnsignedAndMalformed(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	if err := (&SignalMessage{Type: SignalOffer}).Verify(now); !errors.Is(err, ErrSignalUnsigned) {
		t.Fatalf("unsigned: want ErrSignalUnsigned, got %v", err)
	}
	m := &SignalMessage{Type: SignalOffer, PubKey: "not base64!", Nonce: "n", Sig: "s"}
	if err := m.Verify(now); !errors.Is(err, ErrSignalBadKey) {
		t.Fatalf("bad key: want ErrSignalBadKey, got %v", err)
	}
}

// An unsigned message must encode exactly as it did before these fields
// existed, or every current receiver sees a changed wire format.
func TestSignedSignal_UnsignedWireFormatUnchanged(t *testing.T) {
	data, err := (&SignalMessage{Type: SignalOffer, SessionID: "s", SDP: "v=0"}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := `{"type":"offer","session_id":"s","sdp":"v=0"}`; string(data) != want {
		t.Fatalf("unsigned encoding changed:\n got %s\nwant %s", data, want)
	}
}

func TestNonceCache(t *testing.T) {
	_, priv := newKey(t)
	now := time.Unix(1_790_000_000, 0)
	m := signedOffer(t, priv, now)
	c := NewNonceCache()

	if !c.CheckAndRecord(m, now) {
		t.Fatal("first sight of a nonce must be accepted")
	}
	if c.CheckAndRecord(m, now.Add(30*time.Second)) {
		t.Fatal("replay inside the validity window must be refused")
	}

	// Another sender's identical nonce is a different message.
	_, otherPriv := newKey(t)
	o := signedOffer(t, otherPriv, now)
	o.Nonce = m.Nonce
	if !c.CheckAndRecord(o, now) {
		t.Fatal("nonces must be scoped to the signing key")
	}

	// Past expiry the entry is dropped; Verify is what refuses it then.
	c.CheckAndRecord(&SignalMessage{PubKey: "x", Nonce: "y", ExpiresAt: now.Add(10 * time.Minute).Unix()}, now.Add(2*time.Minute))
	c.mu.Lock()
	n := len(c.seen)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("expired entries should be pruned, %d remain", n)
	}
}
