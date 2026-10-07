// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The hash must follow NIP-01's escaping to the byte, or no other client or
// relay would accept our events. The expected value was computed
// independently, with Python's json.dumps(ensure_ascii=False,
// separators=(",", ":")), whose escaping is exactly NIP-01's.
func TestEventHashFollowsNIP01Escaping(t *testing.T) {
	ev := Event{
		PubKey:    "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		CreatedAt: 1700000000,
		Kind:      KindSignal,
		Tags:      [][]string{{"p", "ab<>&"}, {"expiration", "123"}},
		Content:   "quote \" backslash \\ nl \n cr \r tab \t bs \b ff \f nul \x00 esc \x1b lt < gt > amp & ls   ps   é 🍕 del \x7f",
	}
	h := ev.hash()
	if got, want := hex.EncodeToString(h[:]), "15bf5cab0adec083d1a9b9882d3418c147a617c045c97cd2b0a6b0405db47ed6"; got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestEventSignVerify(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{CreatedAt: time.Now().Unix(), Kind: 1, Tags: [][]string{}, Content: "hello"}
	if err := ev.sign(k); err != nil {
		t.Fatal(err)
	}
	if err := ev.verify(); err != nil {
		t.Fatalf("own event: %v", err)
	}
	// It must survive a JSON round trip, as it does through a relay.
	data, _ := json.Marshal(ev)
	var back Event
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.verify(); err != nil {
		t.Fatalf("after JSON round trip: %v", err)
	}

	tampered := ev
	tampered.Content = "hellO"
	if err := tampered.verify(); !errors.Is(err, errBadID) {
		t.Errorf("tampered content: %v, want errBadID", err)
	}

	other, _ := GenerateKey()
	forged := ev
	forged.PubKey = other.PublicKey()
	h := forged.hash()
	forged.ID = hex.EncodeToString(h[:])
	if err := forged.verify(); !errors.Is(err, errBadSig) {
		t.Errorf("signature from another key: %v, want errBadSig", err)
	}
}

func TestKeysRefuseBadEncodings(t *testing.T) {
	k, _ := GenerateKey()
	pub := k.PublicKey()
	if !ValidPublicKey(pub) {
		t.Fatal("own public key refused")
	}
	for _, bad := range []string{"", pub[:62], pub + "00", strings.ToUpper(pub), "zz" + pub[2:]} {
		if ValidPublicKey(bad) {
			t.Errorf("accepted public key %q", bad)
		}
	}
	if _, err := SecretKeyFromBytes(make([]byte, 31)); err == nil {
		t.Error("accepted a 31-byte secret key")
	}
}
