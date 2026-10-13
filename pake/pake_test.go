// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pake

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func config(t *testing.T, secret string) Config {
	t.Helper()
	return Config{
		Secret:    []byte(secret),
		Context:   []byte("remotekit pake test"),
		SessionID: []byte("0123456789abcdef"),
		Key:       newKey(t),
	}
}

func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

// run carries the three messages between a and b directly.
func run(t *testing.T, a, b Config) (Result, Result, error, error) {
	t.Helper()
	ini, start, err := NewInitiator(a)
	if err != nil {
		t.Fatal(err)
	}
	res, reply, err := NewResponder(b, start)
	if err != nil {
		return Result{}, Result{}, nil, err
	}
	confirm, ra, errA := ini.Finish(reply)
	if errA != nil {
		// The initiator sends nothing; the responder never confirms.
		_, errB := res.Finish(nil)
		return ra, Result{}, errA, errB
	}
	rb, errB := res.Finish(confirm)
	return ra, rb, errA, errB
}

func TestTheSameSecretBindsBothKeys(t *testing.T) {
	a, b := config(t, "5071"), config(t, "5071")
	ra, rb, errA, errB := run(t, a, b)
	if errA != nil || errB != nil {
		t.Fatalf("the exchange failed: %v, %v", errA, errB)
	}
	if !ra.PeerKey.Equal(pub(b.Key)) || !rb.PeerKey.Equal(pub(a.Key)) {
		t.Error("an end did not learn the other's key")
	}
	if len(ra.Key) != 32 || !bytes.Equal(ra.Key, rb.Key) {
		t.Errorf("the shared keys differ: %x, %x", ra.Key, rb.Key)
	}
	// A second run under the same inputs gives a new key: scalars are fresh.
	ra2, _, _, _ := run(t, a, b)
	if bytes.Equal(ra.Key, ra2.Key) {
		t.Error("two runs gave the same key")
	}
}

func TestAWrongSecretFailsOnBothEnds(t *testing.T) {
	_, _, errA, errB := run(t, config(t, "5071"), config(t, "5072"))
	if !errors.Is(errA, ErrAuthFailed) {
		t.Errorf("the initiator got %v, want ErrAuthFailed", errA)
	}
	if errB == nil {
		t.Error("the responder accepted an exchange the initiator refused")
	}
}

func TestADifferentContextOrSessionFails(t *testing.T) {
	a, b := config(t, "5071"), config(t, "5071")
	b.Context = []byte("another purpose")
	if _, _, errA, _ := run(t, a, b); !errors.Is(errA, ErrAuthFailed) {
		t.Errorf("a different Context gave %v", errA)
	}
	b = config(t, "5071")
	b.SessionID = []byte("fedcba9876543210")
	if _, _, errA, _ := run(t, a, b); !errors.Is(errA, ErrAuthFailed) {
		t.Errorf("a different SessionID gave %v", errA)
	}
}

// A carrier that swaps the responder's key for its own, without the secret,
// is caught by the initiator: the key is in the transcript the tags cover.
func TestASwappedKeyIsRefused(t *testing.T) {
	a, b := config(t, "5071"), config(t, "5071")
	ini, start, _ := NewInitiator(a)
	_, reply, err := NewResponder(b, start)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := parse(reply, tagReply, 3)
	forged := append([]byte{tagReply}, lvCat(f[0], pub(newKey(t)), f[2])...)
	if _, _, err := ini.Finish(forged); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("a reply with a swapped key gave %v, want ErrAuthFailed", err)
	}

	// And the initiator's key, swapped on the way to the responder.
	ini, start, _ = NewInitiator(a)
	f, _ = parse(start, tagStart, 2)
	swapped := append([]byte{tagStart}, lvCat(f[0], pub(newKey(t)))...)
	res, reply, err := NewResponder(b, swapped)
	if err != nil {
		t.Fatal(err)
	}
	confirm, _, err := ini.Finish(reply)
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("the initiator accepted a reply to a swapped start: %v", err)
	}
	if _, err := res.Finish(confirm); err == nil {
		t.Error("the responder accepted a start whose key was swapped")
	}
}

// A man in the middle runs the exchange with each end under its own key and
// a guessed secret. Without the right secret, both ends fail.
func TestAManInTheMiddleWithoutTheSecretFails(t *testing.T) {
	a, b := config(t, "5071"), config(t, "5071")
	mitm := config(t, "1234")

	ini, start, _ := NewInitiator(a)
	// Toward the initiator, the middle answers as a responder.
	_, toA, err := NewResponder(mitm, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ini.Finish(toA); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("the initiator accepted the middle: %v", err)
	}
	// Toward the responder, the middle starts as an initiator.
	mi, toB, _ := NewInitiator(mitm)
	res, reply, err := NewResponder(b, toB)
	if err != nil {
		t.Fatal(err)
	}
	confirm, _, err := mi.Finish(reply)
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("the middle passed the responder's tag with a wrong secret: %v", err)
	}
	if _, err := res.Finish(confirm); err == nil {
		t.Error("the responder accepted the middle")
	}
}

// A replayed confirmation, or one from another run, is refused.
func TestAConfirmationFromAnotherRunIsRefused(t *testing.T) {
	a, b := config(t, "5071"), config(t, "5071")
	ini1, start1, _ := NewInitiator(a)
	_, reply1, _ := NewResponder(b, start1)
	confirm1, _, err := ini1.Finish(reply1)
	if err != nil {
		t.Fatal(err)
	}
	_, start2, _ := NewInitiator(a)
	res2, _, _ := NewResponder(b, start2)
	if _, err := res2.Finish(confirm1); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("another run's confirmation gave %v", err)
	}
	if _, err := res2.Finish(confirm1); err == nil {
		t.Error("a responder finished twice")
	}
}

func TestTheIdentityElementIsRefused(t *testing.T) {
	b := config(t, "5071")
	start := append([]byte{tagStart}, lvCat(make([]byte, 32), pub(newKey(t)))...)
	if _, _, err := NewResponder(b, start); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("a start with Ya = G.I gave %v", err)
	}
}

func TestMalformedMessagesAreRefused(t *testing.T) {
	b := config(t, "5071")
	for _, msg := range [][]byte{
		nil,
		{tagReply},
		{tagStart},
		append([]byte{tagStart}, lvCat(make([]byte, 31), pub(newKey(t)))...),
		append([]byte{tagStart}, lvCat(make([]byte, 32))...),
		append(append([]byte{tagStart}, lvCat(make([]byte, 32), pub(newKey(t)))...), 0x05),
		{tagStart, 0xff, 0xff, 0xff},
	} {
		if _, _, err := NewResponder(b, msg); !errors.Is(err, ErrMalformed) {
			t.Errorf("NewResponder(%x) = %v, want ErrMalformed", msg, err)
		}
	}
	ini, _, _ := NewInitiator(config(t, "5071"))
	if _, _, err := ini.Finish([]byte{tagConfirm}); !errors.Is(err, ErrMalformed) {
		t.Errorf("Finish(confirm) = %v, want ErrMalformed", err)
	}
}

func TestAConfigNeedsASecretAndAKey(t *testing.T) {
	c := config(t, "")
	if _, _, err := NewInitiator(c); !errors.Is(err, ErrNoSecret) {
		t.Errorf("no secret: %v", err)
	}
	c = config(t, "5071")
	c.Key = c.Key[:10]
	if _, _, err := NewInitiator(c); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("short key: %v", err)
	}
}

func FuzzNewResponder(f *testing.F) {
	f.Add([]byte{tagStart})
	f.Fuzz(func(t *testing.T, msg []byte) {
		_, _, _ = NewResponder(Config{Secret: []byte("5071"), Key: ed25519.NewKeyFromSeed(make([]byte, 32))}, msg)
	})
}
