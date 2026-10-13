// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pake

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/subtle"
	"errors"

	"github.com/gtank/ristretto255"
)

// Errors a caller can act on.
var (
	// ErrAuthFailed means the other end did not prove it holds the same
	// secret: the secret was mistyped, or someone in the middle tried to
	// take part. The two cases cannot be told apart, on purpose.
	ErrAuthFailed = errors.New("pake: the other end does not hold the same secret")
	// ErrMalformed means a message is not one of this protocol's.
	ErrMalformed = errors.New("pake: malformed message")
	// ErrInvalidKey means a Config's signing key is not an ed25519 key.
	ErrInvalidKey = errors.New("pake: the signing key is not an ed25519 private key")
	// ErrNoSecret means a Config has no secret to run the exchange over.
	ErrNoSecret = errors.New("pake: no secret")
)

// Config is one end's inputs. Both ends must give the same Secret, Context
// and SessionID, or the exchange fails as a wrong secret does.
type Config struct {
	// Secret is the low-entropy secret both people share: CPace's PRS. It
	// must never have crossed the carrier.
	Secret []byte
	// Context is CPace's channel identifier, CI: what this exchange is
	// for, so that a secret used for one purpose cannot be replayed into
	// another. It is hashed, never sent.
	Context []byte
	// SessionID is CPace's sid. It should be unique to the session and
	// known to both ends; the draft recommends randomness both contribute.
	SessionID []byte
	// Key is this end's ed25519 signing key. Its public half is what the
	// other end learns, authenticated, as Result.PeerKey.
	Key ed25519.PrivateKey
}

// Result is what a successful exchange gives each end.
type Result struct {
	// PeerKey is the other end's ed25519 public key, proved to come from
	// whoever holds the same secret: the key to pin, e.g. as
	// stream.ViewerConfig.AgentKey or in RequireSignedViewers.
	PeerKey ed25519.PublicKey
	// Key is 32 bytes both ends share and nobody else knows, for whatever
	// the application derives from it.
	Key []byte
}

// Message tags. Each message starts with one, so that a message out of
// place is refused rather than misread.
const (
	tagStart   = 0x01 // initiator → responder: Ya, ADa
	tagReply   = 0x02 // responder → initiator: Yb, ADb, Tb
	tagConfirm = 0x03 // initiator → responder: Ta
)

// macSize is the length of a key-confirmation tag: HMAC-SHA-512.
const macSize = sha512.Size

// Initiator is the end that speaks first: in Chirp, the controller, who has
// typed the code.
type Initiator struct {
	cfg Config
	y   *ristretto255.Scalar
	ya  []byte
	ada []byte
}

// NewInitiator starts an exchange. It returns the first message, for the
// responder.
func NewInitiator(cfg Config) (*Initiator, []byte, error) {
	ada, err := checkConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	y, err := sampleScalar(randReader)
	if err != nil {
		return nil, nil, err
	}
	ya := scalarMult(y, calculateGenerator(cfg.Secret, cfg.Context, cfg.SessionID))
	msg := append([]byte{tagStart}, lvCat(ya, ada)...)
	return &Initiator{cfg: cfg, y: y, ya: ya, ada: ada}, msg, nil
}

// Finish reads the responder's reply. If the responder proves it holds the
// same secret, Finish returns the last message, for the responder, and the
// result; otherwise ErrAuthFailed, and nothing is to be sent.
func (i *Initiator) Finish(reply []byte) ([]byte, Result, error) {
	f, ok := parse(reply, tagReply, 3)
	if !ok || len(f[0]) != groupSizeBytes || len(f[1]) != ed25519.PublicKeySize || len(f[2]) != macSize {
		return nil, Result{}, ErrMalformed
	}
	yb, adb, tb := f[0], f[1], f[2]
	k := scalarMultVfy(i.y, yb)
	key, err := isk(i.cfg.SessionID, k, i.ya, i.ada, yb, adb)
	if err != nil {
		return nil, Result{}, ErrAuthFailed
	}
	mk := macKey(i.cfg.SessionID, key)
	if !hmac.Equal(tb, tag(mk, yb, adb)) {
		return nil, Result{}, ErrAuthFailed
	}
	confirm := append([]byte{tagConfirm}, lvCat(tag(mk, i.ya, i.ada))...)
	return confirm, result(adb, key), nil
}

// Responder is the end that answers: in Chirp, the owner, whose code it is.
type Responder struct {
	cfg  Config
	ya   []byte
	ada  []byte
	mk   []byte
	key  []byte
	done bool
}

// NewResponder answers the initiator's first message. It returns the reply,
// for the initiator; the exchange is not authenticated until Finish accepts
// the initiator's confirmation.
func NewResponder(cfg Config, start []byte) (*Responder, []byte, error) {
	adb, err := checkConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	f, ok := parse(start, tagStart, 2)
	if !ok || len(f[0]) != groupSizeBytes || len(f[1]) != ed25519.PublicKeySize {
		return nil, nil, ErrMalformed
	}
	ya, ada := f[0], f[1]
	y, err := sampleScalar(randReader)
	if err != nil {
		return nil, nil, err
	}
	yb := scalarMult(y, calculateGenerator(cfg.Secret, cfg.Context, cfg.SessionID))
	k := scalarMultVfy(y, ya)
	key, err := isk(cfg.SessionID, k, ya, ada, yb, adb)
	if err != nil {
		return nil, nil, ErrAuthFailed
	}
	mk := macKey(cfg.SessionID, key)
	reply := append([]byte{tagReply}, lvCat(yb, adb, tag(mk, yb, adb))...)
	return &Responder{cfg: cfg, ya: ya, ada: ada, mk: mk, key: key}, reply, nil
}

// Finish reads the initiator's confirmation. It returns the result if the
// initiator proves it holds the same secret, and ErrAuthFailed otherwise.
// A Responder finishes once; a second call fails.
func (r *Responder) Finish(confirm []byte) (Result, error) {
	if r.done {
		return Result{}, ErrMalformed
	}
	r.done = true
	f, ok := parse(confirm, tagConfirm, 1)
	if !ok || len(f[0]) != macSize {
		return Result{}, ErrMalformed
	}
	if !hmac.Equal(f[0], tag(r.mk, r.ya, r.ada)) {
		return Result{}, ErrAuthFailed
	}
	return result(r.ada, r.key), nil
}

// checkConfig checks cfg and returns this end's associated data: its ed25519
// public key, which the transcript binds and the other end learns.
func checkConfig(cfg Config) ([]byte, error) {
	if len(cfg.Secret) == 0 {
		return nil, ErrNoSecret
	}
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}
	pub, ok := cfg.Key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, ErrInvalidKey
	}
	return append([]byte(nil), pub...), nil
}

// macKey is the draft's key-confirmation key (section 10.4):
// H.hash(b"CPaceMac" || sid || ISK).
func macKey(sid, isk []byte) []byte {
	h := sha512.New()
	h.Write([]byte("CPaceMac"))
	h.Write(sid)
	h.Write(isk)
	return h.Sum(nil)
}

// tag is a key-confirmation tag over one end's message, as section 10.4 has
// it: MAC(mac_key, lv_cat(Y, AD)).
func tag(macKey, y, ad []byte) []byte {
	m := hmac.New(sha512.New, macKey)
	m.Write(lvCat(y, ad))
	return m.Sum(nil)
}

// result derives what the application gets from ISK. The draft recommends
// running ISK through a KDF before use (section 10.3).
func result(peerKey, isk []byte) Result {
	key, err := hkdf.Key(sha512.New, isk, nil, "remotekit pake v1", 32)
	if err != nil {
		panic("pake: hkdf refused 32 bytes") // cannot happen
	}
	return Result{PeerKey: ed25519.PublicKey(append([]byte(nil), peerKey...)), Key: key}
}

// parse reads a message: its tag, then exactly n length-prefixed fields.
func parse(msg []byte, want byte, n int) ([][]byte, bool) {
	if len(msg) == 0 || msg[0] != want {
		return nil, false
	}
	rest := msg[1:]
	fields := make([][]byte, 0, n)
	for len(rest) > 0 {
		l, used, ok := readLen(rest)
		if !ok || l > len(rest)-used {
			return nil, false
		}
		fields = append(fields, rest[used:used+l])
		rest = rest[used+l:]
	}
	return fields, len(fields) == n
}

// readLen reads a LEB128 length of at most two bytes: no field here is
// longer than 16383 bytes.
func readLen(b []byte) (n, used int, ok bool) {
	for i := 0; i < len(b) && i < 2; i++ {
		n |= int(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return n, i + 1, true
		}
	}
	return 0, 0, false
}

func subtleEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
