// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/input"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/transfer"
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
func startSignalHarness(t *testing.T, r *Runner) *signalHarness {
	t.Helper()
	if r.clip == nil {
		r.clip = clipboard.NewMemoryClipboard()
	}
	if r.newCapturer == nil {
		r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	}
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
		r.runSignalingLoop(ctx, wsConn{ws})
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
		case "frame", "agent_ready", "capture_unavailable", "relay_mode", "relay_fallback_refused":
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

	start := time.Now()
	select {
	case <-h.answered:
	case <-time.After(45 * time.Second):
		h.t.Fatal("the agent did not answer within 45s")
	}
	deadline := time.Now().Add(45 * time.Second)
	for !viewer.DataChannelOpen(webrtc.DataChannelControl) || !viewer.DataChannelOpen(webrtc.DataChannelTransfer) {
		if time.Now().After(deadline) {
			h.t.Fatalf("data channels not open within 45s (state %v)", viewer.ConnectionState())
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Logf("connection setup took %v", time.Since(start))
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
	r := &Runner{id: "agent-1", injector: inj}
	r.Grant(PermissionFileTransfer) // for the barrier below, not for input
	h := startSignalHarness(t, r)

	barrier := make(chan struct{}, 1)
	viewer := h.connectViewer(func(_ string, msg []byte) {
		if strings.Contains(string(msg), `"file_saved"`) {
			barrier <- struct{}{}
		}
	})

	move := envelope(t, bark.TypeInputEvent, bark.InputEvent{Type: "mouse_move", X: 0.5, Y: 0.5})
	// A chunk for a transfer that was never started is answered with a
	// file_saved refusing it. Messages on one channel are handled in order, so once that reply
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
	r := &Runner{id: "agent-1"}
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
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

	deadline := time.After(10 * time.Second)
	found := false
	for !found {
		select {
		case got := <-replies:
			if got == webrtc.DataChannelTransfer+":file_progress" {
				found = true
				break
			}
			// Unrelated control channel broadcasts (such as bark-control:displays) can arrive concurrently.
			if strings.HasPrefix(got, webrtc.DataChannelTransfer+":") {
				t.Fatalf("reply %q, want %q", got, webrtc.DataChannelTransfer+":file_progress")
			}
		case <-deadline:
			t.Fatal("no file_progress on the data channel within 10s")
		}
	}
	h.expectNothing(200 * time.Millisecond)
}

// Without RequireDataChannel, a viewer that predates the data channel still
// controls the machine over the socket.
func TestSocketInput_StillAcceptedByDefault(t *testing.T) {
	inj := &countingInjector{}
	r := &Runner{id: "agent-1", injector: inj}
	r.Grant(PermissionRemoteControl)
	h := startSignalHarness(t, r)

	h.send(map[string]interface{}{"type": "input", "payload": map[string]interface{}{"type": "mouse_move", "x": 0.5, "y": 0.5}})
	waitMoves(t, inj, 1)
}

// With RequireDataChannel, user data over the socket is refused even when
// consent was given, and the viewer is told why once per message type.
func TestRequireDataChannel_RefusesSocketData(t *testing.T) {
	inj := &countingInjector{}
	r := &Runner{id: "agent-1", injector: inj}
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
	r := &Runner{id: "agent-1", injector: inj}
	r.Grant(PermissionRemoteControl)
	r.RequireDataChannel(true)
	h := startSignalHarness(t, r)
	viewer := h.connectViewer(nil)

	if err := viewer.SendData(webrtc.DataChannelControl, envelope(t, bark.TypeInputEvent, bark.InputEvent{Type: "mouse_move", X: 0.1, Y: 0.1})); err != nil {
		t.Fatal(err)
	}
	waitMoves(t, inj, 1)
}

// wsConn adapts a test's WebSocket to SignalConn, as package tunnel does for
// the control plane's.
type wsConn struct{ ws *websocket.Conn }

func (c wsConn) ReadMessage() ([]byte, error) {
	_, data, err := c.ws.ReadMessage()
	return data, err
}

func (c wsConn) WriteMessage(data []byte) error {
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c wsConn) Close() error { return c.ws.Close() }

// A refused transfer is told so once, however many of its chunks were already
// in flight. Each transfer is told on its own, and a transfer is its sender
// and its ID together: one viewer reusing another's ID still gets its reply.
func TestRefusedTransferIsToldOnce(t *testing.T) {
	d := &dataPlane{r: &Runner{id: "agent-1"}} // file_transfer not granted
	var replies []string
	ctx := context.Background()
	send := func(from sender, id string) {
		reply := func(msgType string, fields map[string]interface{}) {
			tid, _ := fields["transfer_id"].(string)
			replies = append(replies, from.id+":"+msgType+":"+tid)
		}
		d.handle(ctx, from, "file_start", map[string]interface{}{"transfer_id": id, "name": "a.bin", "size": float64(3)}, reply)
		for i := 0; i < 5; i++ {
			d.handle(ctx, from, "file_chunk", map[string]interface{}{"transfer_id": id, "index": float64(i), "data": "YQ=="}, reply)
		}
		d.handle(ctx, from, "file_complete", map[string]interface{}{"transfer_id": id}, reply)
	}

	v1, v2 := sender{id: "viewer-1"}, sender{id: "viewer-2"}
	send(v1, "t1")
	send(v1, "t2")
	send(v2, "t1")
	send(v1, "t1")

	want := []string{"viewer-1:file_saved:t1", "viewer-1:file_saved:t2", "viewer-2:file_saved:t1"}
	if strings.Join(replies, " ") != strings.Join(want, " ") {
		t.Fatalf("replies = %v, want %v", replies, want)
	}
}

// The refusal record holds a fixed-size key per transfer, whatever the length
// of the IDs, and fields that only concatenate alike stay apart.
func TestRefusalKey(t *testing.T) {
	if refusalKey(sender{id: "a"}, "bc") == refusalKey(sender{id: "ab"}, "c") {
		t.Fatal("refusal keys collide when the field boundary moves")
	}
	if refusalKey(sender{id: "v", key: []byte{1}}, "t") == refusalKey(sender{id: "v"}, "t") {
		t.Fatal("refusal keys ignore the sender's key")
	}

	var in fileInbox
	long := strings.Repeat("x", 1<<20)
	for i := 0; i < maxRefused*2; i++ {
		in.refuseFile(sender{id: "v"}, fmt.Sprint(i)+long, "refused", func(string, map[string]interface{}) {})
	}
	if n := len(in.refused); n > maxRefused {
		t.Fatalf("refused holds %d transfers, want at most %d", n, maxRefused)
	}
}

func TestConfig_RequireDataChannel(t *testing.T) {
	inj := &countingInjector{}
	r := New(Config{
		ID:                 "agent-1",
		RequireDataChannel: true,
	})
	r.injector = inj
	r.Grant(PermissionRemoteControl)
	h := startSignalHarness(t, r)

	h.send(map[string]interface{}{"type": "input", "payload": map[string]interface{}{"type": "mouse_move", "x": 0.5, "y": 0.5}})
	m := h.expect("data_channel_required", 5*time.Second)
	if m["message_type"] != "input" {
		t.Fatalf("expected data_channel_required for input, got %v", m)
	}
	if got := inj.moves.Load(); got != 0 {
		t.Fatalf("expected 0 injected moves, got %d", got)
	}
}

func TestFileSaved_OmitHostPathInReply(t *testing.T) {
	tmpDir := t.TempDir()
	tm, err := transfer.NewManager(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("secret contents")
	h := sha256.Sum256(content)
	contentHash := hex.EncodeToString(h[:])

	sess := tm.StartSession("sess-1", "secret.txt", int64(len(content)), contentHash)
	if sess == nil {
		t.Fatal("expected session")
	}
	_, _, err = tm.AddChunk(sess.ID, 0, content)
	if err != nil {
		t.Fatal(err)
	}

	in := fileInbox{
		transfer: tm,
		omitPath: true,
	}

	var savedReceived ReceivedFile
	var replyType string
	var replyFields map[string]interface{}

	in.handle(
		sender{id: "viewer-1"},
		"file_complete",
		map[string]interface{}{"transfer_id": sess.ID},
		"agent",
		func(stage string) string { return "" },
		func(FileOffer) bool { return true },
		true,
		func(rf ReceivedFile) { savedReceived = rf },
		func(rt string, rf map[string]interface{}) {
			replyType = rt
			replyFields = rf
		},
	)

	if replyType != "file_saved" {
		t.Fatalf("expected reply file_saved, got %q", replyType)
	}

	pathInReply, ok := replyFields["path"].(string)
	if !ok {
		t.Fatalf("expected path in replyFields, got %v", replyFields)
	}

	// When omitPath is true, path must only be the bare filename, NOT containing directory separators or tmpDir.
	if pathInReply != "secret.txt" {
		t.Fatalf("expected path in reply to be bare filename %q, got %q", "secret.txt", pathInReply)
	}
	if strings.Contains(pathInReply, "/") || strings.Contains(pathInReply, "\\") {
		t.Fatalf("path in reply %q contains directory separators", pathInReply)
	}

	// On the host side, savedReceived MUST have the full path.
	if savedReceived.Path != filepath.Join(tmpDir, "secret.txt") {
		t.Fatalf("expected local OnFileSaved callback to have full path %q, got %q", filepath.Join(tmpDir, "secret.txt"), savedReceived.Path)
	}
}

