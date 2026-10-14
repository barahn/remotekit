// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"crypto/ed25519"
	"sync"
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
// in between stays in the same visit. Events arrive one at a time, in order,
// on a goroutine of the session's own and never while the runner holds a
// lock, so fn may call Disconnect; a slow fn delays later events, not the
// session.
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

// sessionHook is how Disconnect, and a change of permissions, reach a running
// Serve.
type sessionHook struct {
	disconnect  func(viewerID string) bool
	permissions func()
}

// registerSession makes a running Serve reachable by Disconnect and
// announcePermissions until the returned function is called.
func (r *Runner) registerSession(disconnect func(string) bool, permissions func()) func() {
	h := &sessionHook{disconnect: disconnect, permissions: permissions}
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

// announcePermissions tells the viewers of every running Serve what is
// granted now. It is called after the consent lock is released, since each
// Serve reads the permissions again.
func (r *Runner) announcePermissions() {
	r.sessionsMu.Lock()
	hooks := make([]*sessionHook, 0, len(r.sessions))
	for h := range r.sessions {
		hooks = append(hooks, h)
	}
	r.sessionsMu.Unlock()
	for _, h := range hooks {
		if h.permissions != nil {
			h.permissions()
		}
	}
}

func (r *Runner) emitViewer(ev ViewerEvent) {
	if r.onViewer != nil {
		r.onViewer(ev)
	}
}

// eventQueue delivers ViewerEvents one at a time, in the order they were
// pushed, on a goroutine of its own. Events are pushed while the runner holds
// its state lock, so their order is the order the state changed in; they are
// delivered outside it, so a handler may call back into the runner.
type eventQueue struct {
	deliver func(ViewerEvent)

	mu      sync.Mutex
	pending []ViewerEvent
	closed  bool
	wake    chan struct{}
	done    chan struct{}
}

func newEventQueue(deliver func(ViewerEvent)) *eventQueue {
	q := &eventQueue{deliver: deliver, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go q.run()
	return q
}

// push queues ev. After close it is dropped: the session is over.
func (q *eventQueue) push(ev ViewerEvent) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.pending = append(q.pending, ev)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// close delivers what is queued, then stops. It returns once the last event
// has been delivered.
func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	<-q.done
}

func (q *eventQueue) run() {
	defer close(q.done)
	for range q.wake {
		for {
			q.mu.Lock()
			if len(q.pending) == 0 {
				closed := q.closed
				q.mu.Unlock()
				if closed {
					return
				}
				break
			}
			ev := q.pending[0]
			q.pending = q.pending[1:]
			q.mu.Unlock()
			q.deliver(ev)
		}
	}
}
