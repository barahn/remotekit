// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pake

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha512"
	"errors"

	"github.com/mendsec/cpace"
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

// Initiator is the end that speaks first: in Chirp, the controller, who has
// typed the code.
type Initiator struct{ c *cpace.Initiator }

// NewInitiator starts an exchange. It returns the first message, for the
// responder.
func NewInitiator(cfg Config) (*Initiator, []byte, error) {
	c, err := cpaceConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	ini, start, err := cpace.NewInitiator(c)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return &Initiator{c: ini}, start, nil
}

// Finish reads the responder's reply. If the responder proves it holds the
// same secret, Finish returns the last message, for the responder, and the
// result; otherwise ErrAuthFailed, and nothing is to be sent.
func (i *Initiator) Finish(reply []byte) ([]byte, Result, error) {
	confirm, r, err := i.c.Finish(reply)
	if err != nil {
		return nil, Result{}, mapErr(err)
	}
	res, err := result(r)
	if err != nil {
		return nil, Result{}, err
	}
	return confirm, res, nil
}

// Responder is the end that answers: in Chirp, the owner, whose code it is.
type Responder struct{ c *cpace.Responder }

// NewResponder answers the initiator's first message. It returns the reply,
// for the initiator; the exchange is not authenticated until Finish accepts
// the initiator's confirmation.
func NewResponder(cfg Config, start []byte) (*Responder, []byte, error) {
	c, err := cpaceConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	res, reply, err := cpace.NewResponder(c, start)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return &Responder{c: res}, reply, nil
}

// Finish reads the initiator's confirmation. It returns the result if the
// initiator proves it holds the same secret, and ErrAuthFailed otherwise.
// A Responder finishes once; a second call fails.
func (r *Responder) Finish(confirm []byte) (Result, error) {
	c, err := r.c.Finish(confirm)
	if err != nil {
		return Result{}, mapErr(err)
	}
	return result(c)
}

// cpaceConfig checks cfg and maps it onto CPace: this end's ed25519 public
// key is its associated data, which the transcript binds and the
// confirmation tags prove.
func cpaceConfig(cfg Config) (cpace.Config, error) {
	if len(cfg.Secret) == 0 {
		return cpace.Config{}, ErrNoSecret
	}
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return cpace.Config{}, ErrInvalidKey
	}
	pub, ok := cfg.Key.Public().(ed25519.PublicKey)
	if !ok {
		return cpace.Config{}, ErrInvalidKey
	}
	return cpace.Config{PRS: cfg.Secret, CI: cfg.Context, SID: cfg.SessionID, AD: pub}, nil
}

// result derives what the application gets. The peer's associated data must
// be an ed25519 public key; ISK goes through HKDF, as the draft recommends
// (section 10.3).
func result(r cpace.Result) (Result, error) {
	if len(r.PeerAD) != ed25519.PublicKeySize {
		return Result{}, ErrMalformed
	}
	key, err := hkdf.Key(sha512.New, r.ISK, nil, "remotekit pake v1", 32)
	if err != nil {
		return Result{}, err
	}
	return Result{PeerKey: ed25519.PublicKey(append([]byte(nil), r.PeerAD...)), Key: key}, nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, cpace.ErrAuthFailed):
		return ErrAuthFailed
	case errors.Is(err, cpace.ErrMalformed):
		return ErrMalformed
	case errors.Is(err, cpace.ErrNoPassword):
		return ErrNoSecret
	}
	return err
}
