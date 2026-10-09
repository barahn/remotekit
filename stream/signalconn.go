// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"errors"
)

// SignalConn is the transport a signalling session runs over: one message in,
// one message out. Serve runs a session over any of them -- the control plane's
// WebSocket (package tunnel), a relay network (signal/nostr), a local
// rendezvous, an in-memory pipe in a test -- without the session logic
// changing.
//
// Messages are whole JSON documents, the same ones the WebSocket carries.
// Implementations must keep message boundaries: one WriteMessage on one side is
// one ReadMessage on the other.
//
// Concurrency: ReadMessage is called from one goroutine at a time, and so is
// WriteMessage, but the two run concurrently with each other. Close may be
// called at any time, including while ReadMessage is blocked, and must make it
// return an error; it may be called more than once.
//
// The transport carries; it does not vouch. Whatever authenticates a viewer --
// RequireSignedViewers, consent -- applies the same over any SignalConn, and a
// transport that cannot be trusted any more than the control plane needs
// exactly those checks turned on.
type SignalConn interface {
	// ReadMessage blocks until the next message arrives. Any error ends the
	// session.
	ReadMessage() ([]byte, error)
	// WriteMessage sends one message.
	WriteMessage(data []byte) error
	// Close ends the connection and unblocks ReadMessage.
	Close() error
}

// ErrNoID is why Serve refuses a runner configured without an ID.
var ErrNoID = errors.New("stream: runner has no session ID")

// Serve runs one signalling session over conn: negotiation, the data plane,
// consent, every check the runner is configured with. It returns when conn
// fails or ctx is cancelled, and closes conn either way.
//
// It does not reconnect: a transport that can fail and come back is the
// caller's to redial, calling Serve again with the new connection. Consent
// granted with Grant outlives the connection; a "close" from the viewer side
// revokes it.
//
// It returns ErrNoID if the runner has no ID, ctx.Err() if ctx was cancelled,
// and nil if the connection ended on its own.
func (r *Runner) Serve(ctx context.Context, conn SignalConn) error {
	if r.id == "" {
		return ErrNoID
	}

	// ReadMessage blocks; closing the connection is what unblocks it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	r.runSignalingLoop(ctx, conn)
	_ = conn.Close()
	return ctx.Err()
}
