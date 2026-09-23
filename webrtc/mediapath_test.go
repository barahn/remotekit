// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// loopbackConfig keeps the negotiation on host candidates. The default config
// points at public STUN, which in a test buys network dependence and flakiness
// without buying anything: both ends are on this machine.
func loopbackConfig() PeerConfig { return PeerConfig{} }

// TestVideoTrackDeliversSamples exercises the media path the agent depends on:
// a video track added before the answer, samples written to it, and RTP
// arriving at a receiver that only ever asked to receive.
//
// It is the closest thing to the browser test that can run unattended. It does
// not prove a browser decodes the stream — that needs a browser, and remains
// open — but it does prove the track is negotiated, the answer carries it, and
// written samples leave as RTP. A missing AddTrack, an answer without a video
// m-line, or a sample written to a nil track all fail here.
func TestVideoTrackDeliversSamples(t *testing.T) {
	sender, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer func() { _ = sender.Close() }()

	if err := sender.CreateVideoTrack("barahn-screen", "screen"); err != nil {
		t.Fatalf("CreateVideoTrack: %v", err)
	}

	// The receiver stands in for the browser, which adds a recvonly video
	// transceiver before offering (see web/src/viewer.ts).
	receiver, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	if _, err := receiver.pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo,
		pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("AddTransceiver: %v", err)
	}

	gotRTP := make(chan struct{})
	var once sync.Once
	receiver.pc.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		if track.Kind() != pion.RTPCodecTypeVideo {
			return
		}
		for {
			if _, _, readErr := track.ReadRTP(); readErr != nil {
				return
			}
			once.Do(func() { close(gotRTP) })
		}
	})

	// Candidates have to be trickled, exactly as production does over the
	// signalling WebSocket: CreateOffer and CreateAnswer return as soon as the
	// local description is set, long before ICE has gathered anything, so the
	// descriptions alone never establish a connection.
	senderCands := newCandidateRelay(sender)
	receiverCands := newCandidateRelay(receiver)
	sender.OnICECandidate(receiverCands.accept)
	receiver.OnICECandidate(senderCands.accept)

	// The browser offers and the agent answers, which is the direction
	// pkg/tunnel/agent_stream.go negotiates in.
	offer, err := receiver.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := sender.CreateAnswer(offer)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	senderCands.ready() // the sender has a remote description now
	if err := receiver.SetRemoteAnswer(answer); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}
	receiverCands.ready()

	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	// A real VP8 key frame, produced by this project's encoder rather than a
	// handful of plausible-looking bytes: pion inspects the payload to build
	// RTP headers, so a fake would be testing the wrong thing.
	sample := vp8KeyFrameForTest(t)

	for {
		select {
		case <-gotRTP:
			return // the receiver saw media
		case <-deadline:
			t.Fatalf("no RTP arrived within 20s (sender state %v, receiver state %v)",
				sender.pc.ConnectionState(), receiver.pc.ConnectionState())
		case <-tick.C:
			if !sender.IsConnected() {
				continue
			}
			if err := sender.WriteVideoSample(sample, 33*time.Millisecond); err != nil {
				t.Fatalf("WriteVideoSample: %v", err)
			}
		}
	}
}

// candidateRelay forwards ICE candidates to a peer, holding back any that
// arrive before that peer has a remote description — AddICECandidate rejects
// those, and gathering starts before the answer comes back.
type candidateRelay struct {
	mu      sync.Mutex
	target  *PeerSession
	pending []string
	open    bool
}

func newCandidateRelay(target *PeerSession) *candidateRelay {
	return &candidateRelay{target: target}
}

func (c *candidateRelay) accept(candidateJSON string) {
	c.mu.Lock()
	if !c.open {
		c.pending = append(c.pending, candidateJSON)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	_ = c.target.AddICECandidate(candidateJSON)
}

func (c *candidateRelay) ready() {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.open = true
	c.mu.Unlock()

	for _, cand := range pending {
		_ = c.target.AddICECandidate(cand)
	}
}
