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
// Input, clipboard and file messages carry no signature of their own, so with
// this requirement on they are refused when they arrive over the signalling
// socket, as RequireDataChannel does, and are accepted only over the data
// channels of a session negotiated by a trusted viewer. Viewers must
// therefore open the bark-control and bark-transfer channels. Consent (see
// Grant) still applies on top.
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

// check verifies one raw viewer message for the agent agentID, and returns
// the key it was signed with.
//
// The nonce is recorded only after everything else passes. Recording an
// unverified nonce would let anyone who can write to the socket burn a
// trusted viewer's nonces in advance.
func (a *viewerAuth) check(raw []byte, agentID string, now time.Time) (ed25519.PublicKey, error) {
	m, err := webrtc.DecodeSignalMessage(raw)
	if err != nil {
		return nil, err
	}
	if err := m.Verify(now); err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(m.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, webrtc.ErrSignalBadKey
	}
	if !a.trust(ed25519.PublicKey(pub)) {
		return nil, ErrViewerUntrusted
	}
	if m.TargetID != agentID {
		return nil, fmt.Errorf("%w: signed for %q", ErrViewerWrongTarget, m.TargetID)
	}
	if !a.nonces.CheckAndRecord(m, now) {
		return nil, ErrViewerReplay
	}
	return ed25519.PublicKey(pub), nil
}
