// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/barahn/remotekit/webrtc"
)

// Errors for a viewer message refused by RequireSignedViewers.
var (
	ErrViewerUntrusted   = errors.New("stream: viewer message is signed by a key that is not trusted")
	ErrViewerWrongTarget = errors.New("stream: viewer message is signed for a different agent")
	ErrViewerReplay      = errors.New("stream: viewer message has already been seen")
)

// viewerAuth checks that a viewer's offers and ICE candidates were signed by
// a key the consumer trusts. See RequireSignedViewers.
type viewerAuth struct {
	trust  func(ed25519.PublicKey) bool
	nonces *webrtc.NonceCache
}

// RequireSignedViewers makes the runner act on a viewer's offer,
// session_start or ICE candidate only when the message is signed with
// webrtc.SignalMessage.Sign by a key trust accepts, targets this agent, and
// has not been seen before. Anything else is dropped and logged.
//
// Without it, the signalling socket is trusted as it always was: whoever can
// write to it -- the control plane, or anything impersonating it -- can open
// a WebRTC session to this agent, and starting one starts screen capture.
// With it, the control plane only carries offers it cannot forge.
//
// trust is the consumer's decision about which viewers may connect. remotekit
// has no notion of an operator, so where those keys come from -- a pairing
// ceremony, a fleet policy, a key the user approved once -- is the product's.
// It must be safe for concurrent use and should answer quickly; it is called
// once per signed message.
//
// A viewer signs with TargetID set to the agent's ID, so a message signed for
// one agent cannot be replayed to another. The viewer_id the server routes on
// is not covered by the signature: the server can still label a genuine offer
// with another viewer's ID, which misroutes the agent's answer but cannot
// start a session the trusted viewer did not ask for.
//
// Only negotiation is covered. Input, clipboard and file messages still ride
// the signalling socket unsigned; they are gated by consent (see Grant) until
// they move to a DataChannel in Phase 1 of docs/implementation-phases.md.
// "close" is accepted unsigned, since all it can do is end the session.
//
// Call it before Serve. Passing nil turns the requirement off.
func (r *Runner) RequireSignedViewers(trust func(ed25519.PublicKey) bool) {
	if trust == nil {
		r.viewerAuth = nil
		return
	}
	r.viewerAuth = &viewerAuth{trust: trust, nonces: webrtc.NewNonceCache()}
}

// check verifies one raw viewer message for the agent agentID.
//
// The nonce is recorded only after everything else passes. Recording an
// unverified nonce would let anyone who can write to the socket burn a
// trusted viewer's nonces in advance.
func (a *viewerAuth) check(raw []byte, agentID string, now time.Time) error {
	m, err := webrtc.DecodeSignalMessage(raw)
	if err != nil {
		return err
	}
	if err := m.Verify(now); err != nil {
		return err
	}
	pub, err := base64.StdEncoding.DecodeString(m.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return webrtc.ErrSignalBadKey
	}
	if !a.trust(ed25519.PublicKey(pub)) {
		return ErrViewerUntrusted
	}
	if m.TargetID != agentID {
		return fmt.Errorf("%w: signed for %q", ErrViewerWrongTarget, m.TargetID)
	}
	if !a.nonces.CheckAndRecord(m, now) {
		return ErrViewerReplay
	}
	return nil
}
