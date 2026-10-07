// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memConn is one end of an in-memory SignalConn pair: no network, no
// WebSocket, which is the point -- the session must not depend on either.
type memConn struct {
	in, out chan []byte
	done    chan struct{}
	// closeOnce is shared by both ends: closing either closes the pipe.
	closeOnce *sync.Once
}

var errMemClosed = errors.New("memconn: closed")

func memPipe() (agent, viewer *memConn) {
	a2v, v2a := make(chan []byte, 64), make(chan []byte, 64)
	done, once := make(chan struct{}), &sync.Once{}
	return &memConn{in: v2a, out: a2v, done: done, closeOnce: once},
		&memConn{in: a2v, out: v2a, done: done, closeOnce: once}
}

func (c *memConn) ReadMessage() ([]byte, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.done:
		return nil, errMemClosed
	}
}

func (c *memConn) WriteMessage(data []byte) error {
	select {
	case c.out <- append([]byte(nil), data...):
		return nil
	case <-c.done:
		return errMemClosed
	}
}

func (c *memConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

// next returns the next message the agent sent other than agent_ready, or nil
// after wait.
func (c *memConn) next(wait time.Duration) []byte {
	deadline := time.After(wait)
	for {
		select {
		case m := <-c.in:
			if bytes.Contains(m, []byte(`"agent_ready"`)) {
				continue
			}
			return m
		case <-deadline:
			return nil
		case <-c.done:
			return nil
		}
	}
}

func newServeRunner() *AgentStreamRunner {
	return &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1"}}
}

// A whole session runs over a transport that is not a WebSocket: the agent
// announces itself and answers a session_start exactly as over the socket.
func TestServe_RunsASessionOverAnyTransport(t *testing.T) {
	agent, viewer := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- newServeRunner().Serve(ctx, agent) }()

	select {
	case m := <-viewer.in:
		if !bytes.Contains(m, []byte(`"agent_ready"`)) {
			t.Fatalf("first message = %s, want agent_ready", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never announced itself over the transport")
	}

	if err := viewer.WriteMessage([]byte(`{"type":"session_start","viewer_id":"v"}`)); err != nil {
		t.Fatal(err)
	}
	if viewer.next(5*time.Second) == nil {
		t.Fatal("session_start over the in-memory transport got no reply")
	}

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx was cancelled")
	}
}

// Cancelling ctx must unblock a ReadMessage that is waiting for a message that
// will never come, and close the transport.
func TestServe_CancelUnblocksAndCloses(t *testing.T) {
	agent, _ := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- newServeRunner().Serve(ctx, agent) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve stayed blocked in ReadMessage after cancel")
	}
	select {
	case <-agent.done:
	default:
		t.Fatal("Serve returned without closing the transport")
	}
}

// When the transport ends on its own, Serve returns nil.
func TestServe_ReturnsWhenTheTransportEnds(t *testing.T) {
	agent, viewer := memPipe()
	errc := make(chan error, 1)
	go func() { errc <- newServeRunner().Serve(context.Background(), agent) }()

	time.Sleep(50 * time.Millisecond)
	_ = viewer.Close()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve after the transport closed = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return when the transport closed")
	}
}

func TestServe_NeedsAnAgentID(t *testing.T) {
	agent, _ := memPipe()
	if err := (&AgentStreamRunner{}).Serve(context.Background(), agent); !errors.Is(err, errNoAgentID) {
		t.Fatalf("want errNoAgentID, got %v", err)
	}
}

// The runner's checks hold over any transport: with signed viewers required,
// an unsigned session_start over the in-memory transport starts nothing.
func TestServe_ChecksApplyOverAnyTransport(t *testing.T) {
	pub, _ := viewerKey(t)
	r := newServeRunner()
	r.RequireSignedViewers(trustOnly(pub))

	agent, viewer := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Serve(ctx, agent) }()

	if err := viewer.WriteMessage([]byte(`{"type":"session_start","viewer_id":"v"}`)); err != nil {
		t.Fatal(err)
	}
	if m := viewer.next(500 * time.Millisecond); m != nil {
		t.Fatalf("unsigned session_start got a reply over the in-memory transport: %s", m)
	}
}
