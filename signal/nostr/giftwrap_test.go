// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func twoKeys(t *testing.T) (a, b *SecretKey) {
	t.Helper()
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err = GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestGiftWrapRoundTrip(t *testing.T) {
	alice, bob := twoKeys(t)
	now := time.Now()
	gift, err := wrap(`{"type":"offer"}`, alice, bob.PublicKey(), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// What a relay sees: a one-time key, the recipient, an expiry -- and
	// nothing of the sender or the message.
	if gift.PubKey == alice.PublicKey() {
		t.Error("gift wrap is signed by the sender's key")
	}
	if gift.tag("p") != bob.PublicKey() {
		t.Errorf("p tag = %q, want the recipient", gift.tag("p"))
	}
	exp, err := strconv.ParseInt(gift.tag("expiration"), 10, 64)
	if err != nil || exp != now.Add(time.Minute).Unix() {
		t.Errorf("expiration = %q, want now+ttl", gift.tag("expiration"))
	}
	if gift.CreatedAt > now.Unix() || gift.CreatedAt < now.Add(-wrapJitter).Unix() {
		t.Errorf("created_at %d outside the backdating window", gift.CreatedAt)
	}
	raw, _ := json.Marshal(gift)
	if strings.Contains(string(raw), alice.PublicKey()) || strings.Contains(string(raw), "offer") {
		t.Error("gift wrap leaks the sender or the content")
	}

	sender, content, err := unwrap(gift, bob, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if sender != alice.PublicKey() || content != `{"type":"offer"}` {
		t.Errorf("unwrap = (%s, %s)", sender, content)
	}
}

func TestUnwrapRefusesAnotherRecipient(t *testing.T) {
	alice, bob := twoKeys(t)
	_, eve := twoKeys(t)
	gift, _ := wrap("x", alice, bob.PublicKey(), time.Now(), time.Minute)
	if _, _, err := unwrap(gift, eve, time.Now(), time.Minute); !errors.Is(err, errNotForUs) {
		t.Fatalf("want errNotForUs, got %v", err)
	}
	// Retagging it for eve breaks the signature, and eve still cannot read it.
	gift.Tags[0][1] = eve.PublicKey()
	if _, _, err := unwrap(gift, eve, time.Now(), time.Minute); err == nil {
		t.Fatal("retagged gift wrap was opened")
	}
}

func TestUnwrapRefusesStaleAndFutureMessages(t *testing.T) {
	alice, bob := twoKeys(t)
	now := time.Now()
	for _, written := range []time.Time{now.Add(-2 * time.Minute), now.Add(2 * time.Minute)} {
		gift, _ := wrap("x", alice, bob.PublicKey(), written, time.Minute)
		if _, _, err := unwrap(gift, bob, now, time.Minute); !errors.Is(err, errStaleMessage) {
			t.Errorf("message written at %v: want errStaleMessage, got %v", written.Sub(now), err)
		}
	}
}

// A seal signed by one key around a rumor that names another must be refused,
// or anyone could speak as any viewer.
func TestUnwrapRefusesSpoofedSender(t *testing.T) {
	mallory, bob := twoKeys(t)
	victim, _ := twoKeys(t)
	now := time.Now()
	bobPub, _ := parsePublicKey(bob.PublicKey())

	rumor := Event{CreatedAt: now.Unix(), Kind: KindSignal, Tags: [][]string{}, Content: "x"}
	rumor.setID(victim)
	rumorJSON, _ := json.Marshal(rumor)
	sealed, _ := nip44Encrypt(rumorJSON, conversationKey(mallory, bobPub))
	seal := Event{CreatedAt: now.Unix(), Kind: KindSeal, Tags: [][]string{}, Content: sealed}
	if err := seal.sign(mallory); err != nil {
		t.Fatal(err)
	}
	sealJSON, _ := json.Marshal(seal)
	oneTime, _ := GenerateKey()
	wrapped, _ := nip44Encrypt(sealJSON, conversationKey(oneTime, bobPub))
	gift := Event{CreatedAt: now.Unix(), Kind: KindGiftWrap, Tags: [][]string{{"p", bob.PublicKey()}}, Content: wrapped}
	if err := gift.sign(oneTime); err != nil {
		t.Fatal(err)
	}

	if _, _, err := unwrap(&gift, bob, now, time.Minute); !errors.Is(err, errSenderSpoof) {
		t.Fatalf("want errSenderSpoof, got %v", err)
	}
}

// The double wrap costs space; a message must still fit up to a useful size,
// and one too big must fail cleanly rather than reach a relay.
func TestWrapSizeLimit(t *testing.T) {
	alice, bob := twoKeys(t)
	// An SDP offer is a few kilobytes of JSON with escaped line breaks.
	sdp := strings.Repeat(`a=candidate:1 1 udp 2130706431 192.0.2.1 50000 typ host\r\n`, 400)
	msg := `{"type":"offer","sdp":"` + sdp + `"}`
	if len(msg) < 20_000 {
		t.Fatalf("test message is only %d bytes", len(msg))
	}
	if _, err := wrap(msg, alice, bob.PublicKey(), time.Now(), time.Minute); err != nil {
		t.Fatalf("%d-byte message: %v", len(msg), err)
	}
	if _, err := wrap(strings.Repeat("x", 60_000), alice, bob.PublicKey(), time.Now(), time.Minute); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("60 KB message: want ErrMessageTooLarge, got %v", err)
	}
}
