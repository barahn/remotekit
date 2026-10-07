// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Event kinds this package uses.
const (
	// KindSeal is NIP-59's seal: the rumor, encrypted to the recipient and
	// signed by the real sender.
	KindSeal = 13
	// KindGiftWrap is NIP-59's gift wrap: the seal, encrypted again and
	// signed by a one-time key, so relays do not learn the sender.
	KindGiftWrap = 1059
	// KindSignal is the rumor kind that carries one signalling message. It
	// is our own, in the ephemeral range, and only the two ends ever see it.
	KindSignal = 21059
)

// Event is a NIP-01 event. For a rumor, Sig is empty.
type Event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig,omitempty"`
}

var (
	errBadID  = errors.New("nostr: event id does not match its content")
	errBadSig = errors.New("nostr: bad event signature")
)

// hash returns the NIP-01 event hash: SHA-256 over
// [0,pubkey,created_at,kind,tags,content] serialised exactly as NIP-01
// specifies. encoding/json is not used, because it escapes characters NIP-01
// requires verbatim (<, >, &, U+2028, U+2029) and the hash would then differ
// from every other implementation's.
func (e *Event) hash() [32]byte {
	var b bytes.Buffer
	b.WriteString(`[0,`)
	writeString(&b, e.PubKey)
	b.WriteByte(',')
	b.WriteString(strconv.FormatInt(e.CreatedAt, 10))
	b.WriteByte(',')
	b.WriteString(strconv.Itoa(e.Kind))
	b.WriteString(`,[`)
	for i, tag := range e.Tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		for j, v := range tag {
			if j > 0 {
				b.WriteByte(',')
			}
			writeString(&b, v)
		}
		b.WriteByte(']')
	}
	b.WriteString(`],`)
	writeString(&b, e.Content)
	b.WriteByte(']')
	return sha256.Sum256(b.Bytes())
}

// writeString writes s as a JSON string with NIP-01's escaping: the seven
// short escapes, \u00XX for the remaining control characters, and everything
// else verbatim.
func writeString(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// setID fills in PubKey and ID for key, leaving the event unsigned: a rumor.
func (e *Event) setID(key *SecretKey) {
	e.PubKey = key.PublicKey()
	h := e.hash()
	e.ID = hex.EncodeToString(h[:])
}

// sign fills in PubKey, ID and Sig.
func (e *Event) sign(key *SecretKey) error {
	e.setID(key)
	h := e.hash()
	sig, err := schnorr.Sign(key.priv, h[:])
	if err != nil {
		return err
	}
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

// checkID reports whether ID is the hash of the event's content. It is all
// the integrity a rumor has; a signed event also needs verify.
func (e *Event) checkID() error {
	h := e.hash()
	if e.ID != hex.EncodeToString(h[:]) {
		return errBadID
	}
	return nil
}

// verify checks the event's ID and its BIP-340 signature by PubKey.
func (e *Event) verify() error {
	if !utf8.ValidString(e.Content) {
		return errBadID
	}
	if err := e.checkID(); err != nil {
		return err
	}
	pub, err := parsePublicKey(e.PubKey)
	if err != nil {
		return err
	}
	raw, err := hex.DecodeString(e.Sig)
	if err != nil {
		return errBadSig
	}
	sig, err := schnorr.ParseSignature(raw)
	if err != nil {
		return errBadSig
	}
	h := e.hash()
	if !sig.Verify(h[:], pub) {
		return errBadSig
	}
	return nil
}

// tag returns the first value of the first tag named name, or "".
func (e *Event) tag(name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}
