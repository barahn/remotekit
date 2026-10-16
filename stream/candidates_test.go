// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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

// tap carries messages between two ends and records each.
type tap struct {
	mu   sync.Mutex
	sent map[string][]map[string]interface{} // by direction: "agent" or "viewer"
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
