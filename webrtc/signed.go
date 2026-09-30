// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A signed SignalMessage authenticates itself: whoever holds the sender's
// public key can check it without trusting whatever carried it. That is the
// property the signalling path needs before it can be anything other than a
// server we trust -- see docs/decentralised-signalling.md.
//
// Signing is opt-in. The fields are omitempty, so a message that was never
// signed encodes exactly as it did before, and a receiver that ignores them
// behaves as it always has.

const (
	// MaxSignalLifetime bounds ExpiresAt-IssuedAt. A long-lived signed offer
	// is a long replay window, and it would force the nonce cache to remember
	// it for as long.
	MaxSignalLifetime = 5 * time.Minute

	// SignalClockSkew is how far in the future IssuedAt may sit before the
	// message is refused. Endpoints' clocks disagree; they should not
	// disagree by more than this.
	SignalClockSkew = 30 * time.Second

	// signalDomain separates these signatures from any other thing the same
	// key might sign. Bump the version if the canonical layout changes.
	signalDomain = "remotekit/signal/v1"
)

var (
	ErrSignalUnsigned    = errors.New("webrtc: signal message is not signed")
	ErrSignalBadKey      = errors.New("webrtc: signal message public key is malformed")
	ErrSignalBadSig      = errors.New("webrtc: signal message signature does not verify")
	ErrSignalWrongSigner = errors.New("webrtc: signal message is signed by an unexpected key")
	ErrSignalNotYetValid = errors.New("webrtc: signal message is issued in the future")
	ErrSignalExpired     = errors.New("webrtc: signal message has expired")
	ErrSignalLifetime    = errors.New("webrtc: signal message lifetime is invalid")
)

// CanonicalBytes returns the exact bytes Sign signs and Verify checks.
//
// It is a length-prefixed concatenation behind a domain tag, not JSON: JSON
// field order and escaping differ between encoders, and a browser verifying a
// Go signature must reproduce these bytes exactly. Every field except Sig is
// covered, PubKey included, so a signature cannot be re-attributed to another
// key.
func (m *SignalMessage) CanonicalBytes() []byte {
	var b bytes.Buffer
	writeField := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s))) // #nosec G115 -- signalling fields are far below 4 GiB
		b.Write(n[:])
		b.WriteString(s)
	}
	writeInt := func(v int64) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(v)) // #nosec G115 -- two's-complement round trip is intended
		b.Write(n[:])
	}

	writeField(signalDomain)
	writeField(string(m.Type))
	writeField(m.SessionID)
	writeField(m.TargetID)
	writeField(m.SDP)
	writeField(m.Candidate)
	writeField(m.Error)
	writeField(m.PubKey)
	writeField(m.Nonce)
	writeInt(m.IssuedAt)
	writeInt(m.ExpiresAt)
	return b.Bytes()
}

// Sign stamps the message with the signer's public key, a fresh nonce and a
// validity window of ttl starting now, then signs it. Any earlier signature
// is replaced.
func (m *SignalMessage) Sign(priv ed25519.PrivateKey, now time.Time, ttl time.Duration) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("webrtc: private key must be %d bytes", ed25519.PrivateKeySize)
	}
	if ttl <= 0 || ttl > MaxSignalLifetime {
		return ErrSignalLifetime
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("webrtc: generating nonce: %w", err)
	}

	pub, _ := priv.Public().(ed25519.PublicKey)
	m.PubKey = base64.StdEncoding.EncodeToString(pub)
	m.Nonce = base64.RawURLEncoding.EncodeToString(nonce[:])
	m.IssuedAt = now.Unix()
	m.ExpiresAt = now.Add(ttl).Unix()
	m.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.CanonicalBytes()))
	return nil
}

// Verify checks that the message is signed by the key in its own PubKey field
// and is inside its validity window.
//
// That proves the message is intact and was produced by whoever holds that
// key -- not that the key is one you trust. A caller deciding whether to act
// on a message wants VerifyFrom, with the key it already knows for the peer.
// Verify does not detect replay either; pair it with a NonceCache.
func (m *SignalMessage) Verify(now time.Time) error {
	if m.Sig == "" || m.PubKey == "" || m.Nonce == "" {
		return ErrSignalUnsigned
	}
	pub, err := base64.StdEncoding.DecodeString(m.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ErrSignalBadKey
	}
	sig, err := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignalBadSig
	}
	if !ed25519.Verify(pub, m.CanonicalBytes(), sig) {
		return ErrSignalBadSig
	}

	// Window checks come after the signature so an attacker cannot learn
	// anything from which one fails on a forged message.
	lifetime := m.ExpiresAt - m.IssuedAt
	if lifetime <= 0 || lifetime > int64(MaxSignalLifetime/time.Second) {
		return ErrSignalLifetime
	}
	if m.IssuedAt > now.Add(SignalClockSkew).Unix() {
		return ErrSignalNotYetValid
	}
	if now.Unix() >= m.ExpiresAt {
		return ErrSignalExpired
	}
	return nil
}

// VerifyFrom is Verify plus a check that the signer is expected. This is the
// call to gate any action on: a valid signature from a stranger's key is
// still a stranger's message.
func (m *SignalMessage) VerifyFrom(expected ed25519.PublicKey, now time.Time) error {
	if err := m.Verify(now); err != nil {
		return err
	}
	if m.PubKey != base64.StdEncoding.EncodeToString(expected) {
		return ErrSignalWrongSigner
	}
	return nil
}

// NonceCache refuses a signed message it has already seen.
//
// It only has to remember a nonce until the message carrying it expires --
// after that Verify refuses the message on its own -- so entries are dropped
// at their ExpiresAt, and MaxSignalLifetime keeps the cache bounded by the
// message rate over five minutes.
type NonceCache struct {
	mu   sync.Mutex
	seen map[string]int64 // key: pubkey + nonce, value: ExpiresAt
}

// NewNonceCache returns an empty cache, safe for concurrent use.
func NewNonceCache() *NonceCache {
	return &NonceCache{seen: make(map[string]int64)}
}

// CheckAndRecord reports whether the message is new, and records it if so.
// Call it only after Verify has succeeded: recording unverified nonces would
// let anyone burn a legitimate sender's nonces in advance.
//
// Nonces are scoped to the signing key, so two senders cannot collide.
func (c *NonceCache) CheckAndRecord(m *SignalMessage, now time.Time) bool {
	key := m.PubKey + "\x00" + m.Nonce
	c.mu.Lock()
	defer c.mu.Unlock()

	nowUnix := now.Unix()
	for k, exp := range c.seen {
		if nowUnix >= exp {
			delete(c.seen, k)
		}
	}
	if _, dup := c.seen[key]; dup {
		return false
	}
	c.seen[key] = m.ExpiresAt
	return true
}
