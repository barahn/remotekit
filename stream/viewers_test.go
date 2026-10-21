// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/screen"
)

// hub joins one agent and several viewers over in-memory pipes, as a
// signalling server would: viewers' messages all reach the agent, and the
// agent's reach the viewer named in viewer_id, or every viewer when none is.
type hub struct {
	agent   *memConn
	hubEnd  *memConn
	mu      sync.Mutex
	viewers map[string]*memConn
}

func newHub(t *testing.T) *hub {
	agentEnd, hubEnd := memPipe()
	h := &hub{agent: agentEnd, viewers: make(map[string]*memConn)}
	go func() {
		for {
			data, err := hubEnd.ReadMessage()
			if err != nil {
				return
			}
			var head struct {
				ViewerID string `json:"viewer_id"`
			}
			_ = json.Unmarshal(data, &head)
			h.mu.Lock()
			for id, c := range h.viewers {
				if head.ViewerID == "" || head.ViewerID == id {
					_ = c.WriteMessage(data)
				}
			}
			h.mu.Unlock()
		}
	}()
	h.hubEnd = hubEnd
	t.Cleanup(func() { _ = hubEnd.Close() })
	return h
}

// connect attaches a viewer and returns its end of the transport.
func (h *hub) connect(id string) SignalConn {
	viewerEnd, hubSide := memPipe()
	h.mu.Lock()
	h.viewers[id] = hubSide
	h.mu.Unlock()
	go func() {
		for {
			data, err := hubSide.ReadMessage()
			if err != nil {
				return
			}
			_ = h.hubEnd.WriteMessage(data)
		}
	}()
	return viewerEnd
}

// events collects a runner's ViewerEvents.
type events struct {
	ch      chan ViewerEvent
	pending []ViewerEvent // other viewers' events, kept for their turn
}

func watch(r *Runner) *events {
	e := &events{ch: make(chan ViewerEvent, 64)}
	r.OnViewer(func(ev ViewerEvent) { e.ch <- ev })
	return e
}

// next returns the next event for viewer id, keeping other viewers' for
// later calls.
func (e *events) next(t *testing.T, id string) ViewerEvent {
	t.Helper()
	for i, ev := range e.pending {
		if ev.ViewerID == id {
			e.pending = append(e.pending[:i], e.pending[i+1:]...)
			return ev
		}
	}
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-e.ch:
			if ev.ViewerID == id {
				return ev
			}
			e.pending = append(e.pending, ev)
		case <-deadline:
			t.Fatalf("no event for %s within 20s", id)
		}
	}
}

func fakeRunner() *Runner {
	r := New(Config{ID: "agent-1"})
	r.clip = clipboard.NewMemoryClipboard()
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	return r
}

func TestViewerEventsInOrder(t *testing.T) {
	r := fakeRunner()
	ev := watch(r)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	v.OnFrame(func(*image.YCbCr) {})

	agentEnd, viewerEnd := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	rdone := make(chan struct{})
	go func() {
		_ = r.Serve(ctx, agentEnd)
		close(rdone)
	}()
	defer func() {
		cancel()
		<-rdone
	}()
	vctx, vcancel := context.WithCancel(context.Background())
	vdone := make(chan struct{})
	go func() { _ = v.Serve(vctx, viewerEnd); close(vdone) }()

	start := time.Now()
	for _, want := range []string{ViewerJoined, ViewerStreaming} {
		got := ev.next(t, "viewer-1")
		if got.Kind != want {
			t.Fatalf("event %q, want %q", got.Kind, want)
		}
		if got.At.Before(start) || got.Key != nil {
			t.Fatalf("event %+v: want a time after the start and no key for an unsigned viewer", got)
		}
	}

	// The viewer ends its session: it sends close on the way out.
	vcancel()
	<-vdone
	if got := ev.next(t, "viewer-1"); got.Kind != ViewerLeft {
		t.Fatalf("after the viewer left: event %q, want %q", got.Kind, ViewerLeft)
	}
}

func TestDisconnectEndsOneViewer(t *testing.T) {
	r := fakeRunner()
	ev := watch(r)
	h := newHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = r.Serve(ctx, h.agent)
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	frames := map[string]chan struct{}{}
	notices := map[string]chan Notice{}
	for _, id := range []string{"alice", "bob"} {
		id := id
		frames[id] = make(chan struct{}, 64)
		notices[id] = make(chan Notice, 8)
		v := NewViewer(ViewerConfig{ID: id, AgentID: "agent-1"})
		v.OnFrame(func(*image.YCbCr) {
			select {
			case frames[id] <- struct{}{}:
			default:
			}
		})
		v.OnNotice(func(n Notice) { notices[id] <- n })
		conn := h.connect(id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = v.Serve(ctx, conn)
		}()
	}
	for _, id := range []string{"alice", "bob"} {
		for _, want := range []string{ViewerJoined, ViewerStreaming} {
			if got := ev.next(t, id); got.Kind != want {
				t.Fatalf("%s: event %q, want %q", id, got.Kind, want)
			}
		}
	}

	if !r.Disconnect("alice") {
		t.Fatal("Disconnect(alice) reported no such viewer")
	}
	if got := ev.next(t, "alice"); got.Kind != ViewerLeft {
		t.Fatalf("alice: event %q, want %q", got.Kind, ViewerLeft)
	}
	select {
	case n := <-notices["alice"]:
		if n.Type != "disconnected" {
			t.Fatalf("alice was told %+v, want disconnected", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("alice was not told she was disconnected")
	}

	// Bob keeps watching.
	for len(frames["bob"]) > 0 {
		<-frames["bob"]
	}
	select {
	case <-frames["bob"]:
	case <-time.After(5 * time.Second):
		t.Fatal("bob stopped receiving frames when alice was disconnected")
	}
	if r.Disconnect("nobody") {
		t.Fatal("Disconnect reported a viewer that was never connected")
	}
}

func TestViewerEventCarriesTheSignedKey(t *testing.T) {
	pub, priv := viewerKey(t)
	r := fakeRunner()
	r.RequireSignedViewers(trustOnly(pub))
	ev := watch(r)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1", SigningKey: priv})
	session(t, r, v)
	if got := ev.next(t, "viewer-1"); got.Kind != ViewerJoined || !bytes.Equal(got.Key, pub) {
		t.Fatalf("event %+v, want joined with the viewer's key", got)
	}
}
