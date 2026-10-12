// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"crypto/ed25519"
	"time"
)

// Who is connected, and since when (barahn/remotekit#97). The UI that shows
// it -- a session bar, a tray icon -- is the consumer's; these are the events
// it is built on, and the one action it needs.

// The kinds of ViewerEvent, in the order a viewer goes through them.
const (
	// ViewerJoined: the viewer's offer was accepted and answered. Nothing is
	// seen yet; the connection is still being set up.
	ViewerJoined = "joined"
	// ViewerStreaming: the viewer reported decoding the screen. From here on
	// the screen is actually being seen.
	ViewerStreaming = "streaming"
	// ViewerLeft: the viewer is gone -- it closed the session, its
	// connection failed, Disconnect removed it, or the signalling connection
	// ended. A viewer that negotiates again while connected has not left.
	ViewerLeft = "left"
)

// ViewerEvent is a change in who is connected to the session.
type ViewerEvent struct {
	// ViewerID is the viewer's viewer_id.
	ViewerID string
	// Kind is ViewerJoined, ViewerStreaming or ViewerLeft.
	Kind string
	// At is when the runner saw it.
	At time.Time
	// Key is the key the viewer's offer was signed with, under
	// RequireSignedViewers; nil otherwise.
	Key ed25519.PublicKey
}

// OnViewer registers fn for every ViewerEvent. Each visit is joined, then
// streaming once the screen is seen, then left; a viewer that re-negotiates
// in between stays in the same visit. fn runs on the runner's goroutines, never while
// it holds a lock, so it may call Disconnect; it must not block.
//
// Call it before Serve.
func (r *Runner) OnViewer(fn func(ViewerEvent)) { r.onViewer = fn }

// Disconnect ends one viewer's connection, and tells it with
// {"type":"disconnected"}, without ending the session for anyone else. It
// reports whether that viewer was connected.
//
// It does not keep the viewer out: it can offer again. To refuse it as well,
// Revoke what it was granted, or stop trusting its key under
// RequireSignedViewers.
func (r *Runner) Disconnect(viewerID string) bool {
	r.sessionsMu.Lock()
	hooks := make([]*sessionHook, 0, len(r.sessions))
	for h := range r.sessions {
		hooks = append(hooks, h)
	}
	r.sessionsMu.Unlock()
	found := false
	for _, h := range hooks {
		if h.disconnect(viewerID) {
			found = true
		}
	}
	return found
}

// sessionHook is how Disconnect reaches a running Serve.
type sessionHook struct {
	disconnect func(viewerID string) bool
}

// registerSession makes a running Serve reachable by Disconnect until the
// returned function is called.
func (r *Runner) registerSession(disconnect func(string) bool) func() {
	h := &sessionHook{disconnect: disconnect}
	r.sessionsMu.Lock()
	if r.sessions == nil {
		r.sessions = make(map[*sessionHook]struct{})
	}
	r.sessions[h] = struct{}{}
	r.sessionsMu.Unlock()
	return func() {
		r.sessionsMu.Lock()
		delete(r.sessions, h)
		r.sessionsMu.Unlock()
	}
}

func (r *Runner) emitViewer(ev ViewerEvent) {
	if r.onViewer != nil {
		r.onViewer(ev)
	}
}
