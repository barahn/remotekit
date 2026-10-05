// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/input"
	"github.com/barahn/remotekit/webrtc"
	"github.com/gorilla/websocket"
)

// countingInjector counts mouse moves, safely: data channel messages are
// injected from pion's goroutine, not the signalling loop's.
type countingInjector struct {
	input.Injector
	moves atomic.Int32
}

func (i *countingInjector) MoveMouse(x, y float64) error { i.moves.Add(1); return nil }

// signalHarness plays the server: it holds the agent's end of the signalling
// socket, so a test can write to the agent and read what it sends back.
type signalHarness struct {
	t       *testing.T
	writeMu sync.Mutex
	conn    *websocket.Conn
	// fromAgent receives every message the agent sends except the
	// negotiation, which the harness handles itself when a viewer is attached.
	fromAgent chan map[string]interface{}
	viewer    *webrtc.PeerSession

	answered  chan struct{}
	candMu    sync.Mutex
	held      []string
	answerSet bool
}

// startSignalHarness runs the runner's signalling loop against a fake server
// until the test ends.
func startSignalHarness(t *testing.T, r *AgentStreamRunner) *signalHarness {
	t.Helper()
	h := &signalHarness{
		t:         t,
		fromAgent: make(chan map[string]interface{}, 256),
		answered:  make(chan struct{}),
	}
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		accepted <- c
	}))
	t.Cleanup(srv.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.conn = <-accepted

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.runSignalingLoop(ctx, ws, nil)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		_ = ws.Close()
		_ = h.conn.Close()
		<-done
	})

	go h.read()
	return h
}

func (h *signalHarness) send(msg map[string]interface{}) {
	h.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := h.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		h.t.Errorf("writing to the agent: %v", err)
	}
}

func (h *signalHarness) read() {
	for {
		_, data, err := h.conn.ReadMessage()
		if err != nil {
			return
		}
		var m map[string]interface{}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m["type"] {
		case "answer":
			sdp, _ := m["sdp"].(string)
			if err := h.viewer.SetRemoteAnswer(sdp); err != nil {
				h.t.Errorf("SetRemoteAnswer: %v", err)
				continue
			}
			h.candMu.Lock()
			held := h.held
			h.held, h.answerSet = nil, true
			h.candMu.Unlock()
			for _, c := range held {
				_ = h.viewer.AddICECandidate(c)
			}
			close(h.answered)
		case "candidate":
			// The agent gathers as soon as its answer is set, so its first
			// candidates can overtake the answer on the way here.
			c, _ := m["candidate"].(string)
			h.candMu.Lock()
			if !h.answerSet {
				h.held = append(h.held, c)
				h.candMu.Unlock()
				continue
			}
			h.candMu.Unlock()
			_ = h.viewer.AddICECandidate(c)
		case "frame", "agent_ready", "capture_unavailable":
			// Screen traffic, whose presence depends on the machine.
		default:
			h.fromAgent <- m
		}
	}
}

// connectViewer negotiates a viewer that opens both data channels, the way
// a product's viewer will, and waits until the agent's ends are open.
func (h *signalHarness) connectViewer(onData func(label string, msg []byte)) *webrtc.PeerSession {
	h.t.Helper()
	viewer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = viewer.Close() })
	h.viewer = viewer

	for _, label := range []string{webrtc.DataChannelControl, webrtc.DataChannelTransfer} {
		if err := viewer.OpenDataChannel(label); err != nil {
			h.t.Fatal(err)
		}
	}
	if onData != nil {
		viewer.OnDataMessage(onData)
	}
	viewer.OnICECandidate(func(c string) {
		h.send(map[string]interface{}{"type": "candidate", "viewer_id": "viewer-a", "candidate": c})
	})

	offer, err := viewer.CreateOffer()
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(map[string]interface{}{"type": "offer", "viewer_id": "viewer-a", "sdp": offer})

	select {
	case <-h.answered:
	case <-time.After(20 * time.Second):
		h.t.Fatal("the agent did not answer within 20s")
	}
	deadline := time.Now().Add(20 * time.Second)
	for !viewer.DataChannelOpen(webrtc.DataChannelControl) || !viewer.DataChannelOpen(webrtc.DataChannelTransfer) {
		if time.Now().After(deadline) {
			h.t.Fatalf("data channels not open within 20s (state %v)", viewer.ConnectionState())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return viewer
}

// expectNothing fails if the agent sends anything on the socket within d.
func (h *signalHarness) expectNothing(d time.Duration) {
	h.t.Helper()
	select {
	case m := <-h.fromAgent:
		h.t.Fatalf("the agent sent %v over the signalling socket", m)
	case <-time.After(d):
	}
}

func (h *signalHarness) expect(msgType string, within time.Duration) map[string]interface{} {
	h.t.Helper()
	deadline := time.After(within)
	for {
		select {
		case m := <-h.fromAgent:
			if m["type"] == msgType {
				return m
			}
			h.t.Fatalf("expected %s from the agent, got %v", msgType, m)
		case <-deadline:
			h.t.Fatalf("no %s from the agent within %s", msgType, within)
		}
	}
}

func envelope(t *testing.T, typ bark.MessageType, payload interface{}) []byte {
	t.Helper()
	data, err := bark.EncodeEnvelope(typ, "", payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitMoves(t *testing.T, inj *countingInjector, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for inj.moves.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("got %d mouse moves within 10s, want %d", inj.moves.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Input sent over the viewer's data channel is injected, under the same
// consent rule as input over the socket.
func TestDataChannel_InputNeedsConsent(t *testing.T) {
	inj := &countingInjector{}
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}, injector: inj}
	r.Grant(PermissionFileTransfer) // for the barrier below, not for input
	h := startSignalHarness(t, r)

	barrier := make(chan struct{}, 1)
	viewer := h.connectViewer(func(_ string, msg []byte) {
		if strings.Contains(string(msg), `"file_progress"`) {
			barrier <- struct{}{}
		}
	})

	move := envelope(t, bark.TypeInputEvent, bark.InputEvent{Type: "mouse_move", X: 0.5, Y: 0.5})
	// A chunk for a transfer that was never started still gets a progress
	// reply. Messages on one channel are handled in order, so once that reply
	// is back, the move sent before it has been dealt with.
	ping := envelope(t, bark.TypeFileChunk, bark.FileChunk{TransferID: "none", Index: 0, Data: ""})

	for _, m := range [][]byte{move, ping} {
		if err := viewer.SendData(webrtc.DataChannelControl, m); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-barrier:
	case <-time.After(10 * time.Second):
		t.Fatal("no reply to the barrier within 10s")
	}
	if got := inj.moves.Load(); got != 0 {
		t.Fatalf("input was injected %d times before remote_control was granted", got)
	}

	r.Grant(PermissionRemoteControl)
	if err := viewer.SendData(webrtc.DataChannelControl, move); err != nil {
		t.Fatal(err)
	}
	waitMoves(t, inj, 1)
}

// File transfer over the data channel is answered on the same channel, and
// nothing about it reaches the server.
func TestDataChannel_FileTransferRepliesOnTheChannel(t *testing.T) {
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	r.Grant(PermissionFileTransfer)
	h := startSignalHarness(t, r)

	replies := make(chan string, 4)
	viewer := h.connectViewer(func(label string, msg []byte) {
		var env bark.Envelope
		if json.Unmarshal(msg, &env) == nil {
			replies <- label + ":" + string(env.Type)
		}
	})

	start := envelope(t, bark.TypeFileStart, map[string]interface{}{
		"transfer_id": "t1", "name": "notes.txt", "size": 5,
	})
	chunk := envelope(t, bark.TypeFileChunk, bark.FileChunk{TransferID: "t1", Index: 0, Data: "aGVsbG8="})
	for _, m := range [][]byte{start, chunk} {
		if err := viewer.SendData(webrtc.DataChannelTransfer, m); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case got := <-replies:
		if want := webrtc.DataChannelTransfer + ":file_progress"; got != want {
			t.Fatalf("reply %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no file_progress on the data channel within 10s")
	}
	h.expectNothing(200 * time.Millisecond)
}

// Without RequireDataChannel, a viewer that predates the data channel still
// controls the machine over the socket.
func TestSocketInput_StillAcceptedByDefault(t *testing.T) {
	inj := &countingInjector{}
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}, injector: inj}
	r.Grant(PermissionRemoteControl)
	h := startSignalHarness(t, r)

	h.send(map[string]interface{}{"type": "input", "payload": map[string]interface{}{"type": "mouse_move", "x": 0.5, "y": 0.5}})
	waitMoves(t, inj, 1)
}

// With RequireDataChannel, user data over the socket is refused even when
// consent was given, and the viewer is told why once per message type.
func TestRequireDataChannel_RefusesSocketData(t *testing.T) {
	inj := &countingInjector{}
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}, injector: inj}
	r.Grant(PermissionRemoteControl, PermissionFileTransfer)
	r.RequireDataChannel(true)
	h := startSignalHarness(t, r)

	move := map[string]interface{}{"type": "input", "payload": map[string]interface{}{"type": "mouse_move", "x": 0.5, "y": 0.5}}
	h.send(move)
	h.send(move)
	h.send(map[string]interface{}{"type": "file_start", "transfer_id": "t1", "name": "a.txt", "size": 1})

	if m := h.expect("data_channel_required", 5*time.Second); m["message_type"] != "input" {
		t.Fatalf("first refusal names %v, want input", m["message_type"])
	}
	if m := h.expect("data_channel_required", 5*time.Second); m["message_type"] != "file_start" {
		t.Fatalf("second refusal names %v, want file_start: input should be reported once", m["message_type"])
	}
	if got := inj.moves.Load(); got != 0 {
		t.Fatalf("input over the socket was injected %d times with RequireDataChannel on", got)
	}
}

// With RequireDataChannel, the data channel itself keeps working.
func TestRequireDataChannel_ChannelStillWorks(t *testing.T) {
	inj := &countingInjector{}
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}, injector: inj}
	r.Grant(PermissionRemoteControl)
	r.RequireDataChannel(true)
	h := startSignalHarness(t, r)
	viewer := h.connectViewer(nil)

	if err := viewer.SendData(webrtc.DataChannelControl, envelope(t, bark.TypeInputEvent, bark.InputEvent{Type: "mouse_move", X: 0.1, Y: 0.1})); err != nil {
		t.Fatal(err)
	}
	waitMoves(t, inj, 1)
}
