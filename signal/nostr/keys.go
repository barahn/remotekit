// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// ErrInvalidKey is returned for a secret key outside [1, n) or a public key
// that is not an x-only point on secp256k1.
var ErrInvalidKey = errors.New("nostr: invalid key")

// SecretKey is a secp256k1 secret key, the kind Nostr signs events with.
type SecretKey struct {
	priv *btcec.PrivateKey
}

// GenerateKey returns a fresh random key, for one session.
func GenerateKey() (*SecretKey, error) {
	for {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		if k, err := SecretKeyFromBytes(b[:]); err == nil {
			return k, nil
		}
	}
}

// SecretKeyFromBytes parses a 32-byte secret key. Unlike
// btcec.PrivKeyFromBytes, it refuses a value that is zero or not below the
// curve order instead of silently reducing it.
func SecretKeyFromBytes(b []byte) (*SecretKey, error) {
	if len(b) != 32 {
		return nil, ErrInvalidKey
	}
	var s btcec.ModNScalar
	if overflow := s.SetByteSlice(b); overflow || s.IsZero() {
		return nil, ErrInvalidKey
	}
	return &SecretKey{priv: btcec.PrivKeyFromScalar(&s)}, nil
}

// SecretKeyFromHex parses a secret key written as 64 hex digits.
func SecretKeyFromHex(s string) (*SecretKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return SecretKeyFromBytes(b)
}

// PublicKey returns the x-only public key as 64 lowercase hex digits, the
// form Nostr events and "p" tags carry.
func (k *SecretKey) PublicKey() string {
	return hex.EncodeToString(schnorr.SerializePubKey(k.priv.PubKey()))
}

// parsePublicKey parses an x-only public key written as 64 lowercase hex
// digits. Uppercase is refused rather than folded: keys are compared as
// strings, in "p" tags and between seal and rumor, and one spelling per key
// keeps those comparisons honest.
func parsePublicKey(s string) (*btcec.PublicKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != s {
		return nil, ErrInvalidKey
	}
	pub, err := schnorr.ParsePubKey(b)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return pub, nil
}

// ValidPublicKey reports whether s is an x-only secp256k1 public key in hex,
// such as one a viewer was given to reach an agent.
func ValidPublicKey(s string) bool {
	_, err := parsePublicKey(s)
	return err == nil
}
