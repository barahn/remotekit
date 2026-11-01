// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/webrtc"
)

// candidate is a candidate message from the agent, as the viewer gets it.
func candidate(t *testing.T, offerID string) []byte {
	t.Helper()
	m := webrtc.SignalMessage{Type: webrtc.SignalCandidate, Candidate: `{"candidate":"x"}`}
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["viewer_id"] = "viewer-1"
	if offerID != "" {
		fields["offer_id"] = offerID
	}
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A candidate the agent gathered for an offer the viewer has replaced is
// dropped, as that offer's answer is; one for the current offer, or without
// an offer_id, is kept for the answer (barahn/chirp#109).
func TestViewerDropsCandidatesForAReplacedOffer(t *testing.T) {
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	peer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	v.mu.Lock()
	v.peer, v.offerID, v.answered = peer, "2", false
	v.mu.Unlock()

	pending := func() int {
		v.mu.Lock()
		defer v.mu.Unlock()
		return len(v.pending)
	}
	v.handle(candidate(t, "1"))
	if n := pending(); n != 0 {
		t.Fatalf("a candidate for offer 1 was kept for offer 2's peer (%d pending)", n)
	}
	v.handle(candidate(t, "2"))
	if n := pending(); n != 1 {
		t.Fatalf("a candidate for the current offer: %d pending, want 1", n)
	}
	v.handle(candidate(t, ""))
	if n := pending(); n != 2 {
		t.Fatalf("a candidate without an offer_id, from an older agent: %d pending, want 2", n)
	}
}

// tap carries messages between two ends and records each. With holdOffer
// set, it holds each offer back as long as holdOffer says for its offer_id
// while the messages after it go on, as a transport that does not keep order
// may.
type tap struct {
	mu        sync.Mutex
	sent      map[string][]map[string]interface{} // by direction: "agent" or "viewer"
	holdOffer func(offerID string) time.Duration
}

func (tp *tap) carry(from, to *memConn, dir string, done <-chan struct{}) {
	for {
		m, err := from.ReadMessage()
		if err != nil {
			return
		}
		var fields map[string]interface{}
		if json.Unmarshal(m, &fields) == nil {
			tp.mu.Lock()
			tp.sent[dir] = append(tp.sent[dir], fields)
			tp.mu.Unlock()
		}
		select {
		case <-done:
			return
		default:
		}
		if tp.holdOffer != nil && fields["type"] == "offer" {
			id, _ := fields["offer_id"].(string)
			if hold := tp.holdOffer(id); hold > 0 {
				go func() {
					time.Sleep(hold)
					_ = to.WriteMessage(m)
				}()
				continue
			}
		}
		_ = to.WriteMessage(m)
	}
}

// In a whole session each end tags every candidate with the offer it belongs
// to, and only ever with an offer that was made: the viewer offers twice --
// once at start, again when the agent announces itself -- and the agent
// answers both, so untagged candidates for the first would reach the second's
// peer and be refused for their ICE credentials (barahn/chirp#109).
func TestEveryCandidateNamesItsOffer(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.clip = clipboard.NewMemoryClipboard()
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})

	agentEnd, tapAgent := memPipe()
	tapViewer, viewerEnd := memPipe()
	tp := &tap{sent: map[string][]map[string]interface{}{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); _ = r.Serve(ctx, agentEnd) }()
	go func() { defer wg.Done(); _ = v.Serve(ctx, viewerEnd) }()
	go func() { defer wg.Done(); tp.carry(tapAgent, tapViewer, "agent", done) }()
	go func() { defer wg.Done(); tp.carry(tapViewer, tapAgent, "viewer", done) }()
	defer func() {
		close(done)
		cancel()
		_ = tapAgent.Close()
		_ = tapViewer.Close()
		wg.Wait()
	}()

	// Wait until both ends have sent candidates for an offer that was
	// answered.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tp.mu.Lock()
		a, w := count(tp.sent["agent"], "candidate"), count(tp.sent["viewer"], "candidate")
		tp.mu.Unlock()
		if a > 0 && w > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // the rest of the gathering

	tp.mu.Lock()
	defer tp.mu.Unlock()
	offers := map[string]bool{}
	for _, m := range tp.sent["viewer"] {
		if m["type"] == "offer" {
			offers[m["offer_id"].(string)] = true
		}
	}
	if len(offers) == 0 {
		t.Fatal("the viewer made no offer")
	}
	for dir, msgs := range tp.sent {
		n := 0
		for _, m := range msgs {
			if m["type"] != "candidate" {
				continue
			}
			n++
			id, _ := m["offer_id"].(string)
			if !offers[id] {
				t.Errorf("a candidate from the %s names offer %q; the offers were %v", dir, id, offers)
			}
		}
		if n == 0 {
			t.Errorf("the %s sent no candidate", dir)
		}
	}
}

func count(msgs []map[string]interface{}, typ string) int {
	n := 0
	for _, m := range msgs {
		if m["type"] == typ {
			n++
		}
	}
	return n
}

// Over a transport that does not keep order -- signalling over Nostr relays,
// where each message is a separate event -- a viewer's candidates can reach
// the agent before the offer they belong to. The agent keeps them for that
// offer instead of dropping them: behind TURN, a peer that never learns the
// viewer's candidates never lets its checks through (barahn/chirp#191).
func TestTheAgentKeepsCandidatesThatArriveBeforeTheirOffer(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.clip = clipboard.NewMemoryClipboard()
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	var mu sync.Mutex
	applied := map[string]bool{}
	r.addCandidate = func(peer *webrtc.PeerSession, cand string) error {
		mu.Lock()
		applied[cand] = true
		mu.Unlock()
		return peer.AddICECandidate(cand)
	}
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})

	agentEnd, tapAgent := memPipe()
	tapViewer, viewerEnd := memPipe()
	tp := &tap{sent: map[string][]map[string]interface{}{}, holdOffer: func(string) time.Duration { return 300 * time.Millisecond }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); _ = r.Serve(ctx, agentEnd) }()
	go func() { defer wg.Done(); _ = v.Serve(ctx, viewerEnd) }()
	go func() { defer wg.Done(); tp.carry(tapAgent, tapViewer, "agent", done) }()
	go func() { defer wg.Done(); tp.carry(tapViewer, tapAgent, "viewer", done) }()
	defer func() {
		close(done)
		cancel()
		_ = tapAgent.Close()
		_ = tapViewer.Close()
		wg.Wait()
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tp.mu.Lock()
		answers := count(tp.sent["agent"], "answer")
		tp.mu.Unlock()
		if answers > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second) // the held offers, and the rest of the gathering

	tp.mu.Lock()
	answered := map[string]bool{}
	for _, m := range tp.sent["agent"] {
		if m["type"] == "answer" {
			id, _ := m["offer_id"].(string)
			answered[id] = true
		}
	}
	var sent []string
	for _, m := range tp.sent["viewer"] {
		id, _ := m["offer_id"].(string)
		if m["type"] == "candidate" && answered[id] {
			c, _ := m["candidate"].(string)
			sent = append(sent, c)
		}
	}
	tp.mu.Unlock()
	if len(answered) == 0 || len(sent) == 0 {
		t.Fatalf("%d offers answered, %d candidates for them: nothing to check", len(answered), len(sent))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range sent {
		if !applied[c] {
			t.Errorf("the agent never applied the viewer's candidate %s, which arrived before its offer", c)
		}
	}
}

// Held candidates go to the offer they name, or to any offer when they name
// none; those for an offer that overtook the answered one stay for it.
func TestHeldCandidatesGoToTheirOffer(t *testing.T) {
	h := heldCandidates{}
	h.hold("viewer-1", "2", "c2")
	h.hold("viewer-1", "1", "c1")
	h.hold("viewer-1", "", "old")
	h.hold("viewer-2", "1", "other")
	if got := h.release("viewer-1", "1"); len(got) != 2 || got[0] != "c1" || got[1] != "old" {
		t.Fatalf("offer 1 got %v, want c1 and the one naming no offer", got)
	}
	if got := h.release("viewer-1", "2"); len(got) != 1 || got[0] != "c2" {
		t.Fatalf("offer 2, answered after, got %v, want c2", got)
	}
	if got := h.release("viewer-1", "2"); len(got) != 0 {
		t.Fatalf("released %v twice", got)
	}
	if got := h.release("viewer-2", "1"); len(got) != 1 {
		t.Fatalf("another viewer's candidate: %v", got)
	}
}

// One viewer holds at most maxPendingCandidates, and the newest win: those
// for offers never answered cannot crowd out the current offer's.
func TestHeldCandidatesKeepTheNewest(t *testing.T) {
	h := heldCandidates{}
	for i := range maxPendingCandidates {
		h.hold("viewer-1", "stale", fmt.Sprint(i))
	}
	h.hold("viewer-1", "current", "fresh")
	if n := len(h["viewer-1"]); n != maxPendingCandidates {
		t.Fatalf("holding %d, want at most %d", n, maxPendingCandidates)
	}
	if got := h.release("viewer-1", "current"); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("the current offer got %v, want the newest candidate", got)
	}
}

// The viewer offers again when the agent announces itself. Over a transport
// that does not keep order the first offer can arrive after the second; the
// agent then keeps the peer for the newer one, which is the one the viewer
// still waits on, instead of replacing it with a peer the viewer has
// abandoned (barahn/chirp#191).
func TestTheAgentIgnoresAnOfferOlderThanTheOneItAnswered(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.clip = clipboard.NewMemoryClipboard()
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})

	agentEnd, tapAgent := memPipe()
	tapViewer, viewerEnd := memPipe()
	tp := &tap{sent: map[string][]map[string]interface{}{}, holdOffer: func(id string) time.Duration {
		if id == "1" {
			return 700 * time.Millisecond
		}
		return 0
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); _ = r.Serve(ctx, agentEnd) }()
	go func() { defer wg.Done(); _ = v.Serve(ctx, viewerEnd) }()
	go func() { defer wg.Done(); tp.carry(tapAgent, tapViewer, "agent", done) }()
	go func() { defer wg.Done(); tp.carry(tapViewer, tapAgent, "viewer", done) }()
	defer func() {
		close(done)
		cancel()
		_ = tapAgent.Close()
		_ = tapViewer.Close()
		wg.Wait()
	}()
	time.Sleep(2 * time.Second) // both offers, the held one last

	tp.mu.Lock()
	defer tp.mu.Unlock()
	if count(tp.sent["viewer"], "offer") < 2 {
		t.Skip("the viewer offered once: the agent announced itself before the viewer's first offer")
	}
	var answered []string
	for _, m := range tp.sent["agent"] {
		if m["type"] == "answer" {
			id, _ := m["offer_id"].(string)
			answered = append(answered, id)
		}
	}
	if len(answered) == 0 {
		t.Fatal("the agent answered nothing")
	}
	if last := answered[len(answered)-1]; last != "2" {
		t.Fatalf("the agent answered offers %v, last %q: its peer is for an offer the viewer replaced", answered, last)
	}
}
