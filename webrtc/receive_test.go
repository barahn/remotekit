// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// vp8Packets splits frame into n RTP packets the way RFC 7741 carries it: a
// one-byte payload descriptor on each, with S set on the first.
func vp8Packets(frame []byte, seq uint16, ts uint32, n int) []*rtp.Packet {
	var pkts []*rtp.Packet
	size := (len(frame) + n - 1) / n
	for i := 0; i < n; i++ {
		lo, hi := i*size, (i+1)*size
		if hi > len(frame) {
			hi = len(frame)
		}
		desc := byte(0)
		if i == 0 {
			desc = 0x10 // S: start of partition 0
		}
		pkts = append(pkts, &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: seq + uint16(i),
				Timestamp:      ts,
				Marker:         i == n-1,
			},
			Payload: append([]byte{desc}, frame[lo:hi]...),
		})
	}
	return pkts
}

// key and inter are frames whose first byte marks them as VP8 key and inter
// frames; the rest is filler the assembler does not read.
func key(fill byte) []byte   { return append([]byte{0x00}, bytes.Repeat([]byte{fill}, 30)...) }
func inter(fill byte) []byte { return append([]byte{0x01}, bytes.Repeat([]byte{fill}, 30)...) }

type delivered struct {
	frame []byte
	key   bool
}

// feed pushes pkts in order and returns what was delivered, and whether any
// push reported a loss.
func feed(a *frameAssembler, pkts ...*rtp.Packet) (out []delivered, lost bool) {
	for _, p := range pkts {
		f, k, ok := a.push(p)
		lost = lost || a.lost
		if ok {
			out = append(out, delivered{f, k})
		}
	}
	return out, lost
}

func cat(groups ...[]*rtp.Packet) []*rtp.Packet {
	var all []*rtp.Packet
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

func TestAssemblerDeliversOnTheMarker(t *testing.T) {
	var a frameAssembler
	k := key(1)
	out, lost := feed(&a, vp8Packets(k, 100, 9000, 3)...)
	if lost || len(out) != 1 || !out[0].key || !bytes.Equal(out[0].frame, k) {
		t.Fatalf("got %v (lost %v), want the key frame alone, at once", out, lost)
	}
	in := inter(2)
	out, lost = feed(&a, vp8Packets(in, 103, 12000, 2)...)
	if lost || len(out) != 1 || out[0].key || !bytes.Equal(out[0].frame, in) {
		t.Fatalf("got %v (lost %v), want the inter frame", out, lost)
	}
}

func TestAssemblerWaitsForAKeyFrameFirst(t *testing.T) {
	var a frameAssembler
	out, lost := feed(&a, vp8Packets(inter(1), 100, 9000, 2)...)
	if len(out) != 0 || !lost {
		t.Fatalf("an inter frame with nothing before it: got %v, lost %v; want it withheld and a key frame asked for", out, lost)
	}
	out, _ = feed(&a, vp8Packets(key(2), 102, 12000, 2)...)
	if len(out) != 1 || !out[0].key {
		t.Fatalf("got %v, want the key frame", out)
	}
}

func TestAssemblerPutsAFramesPacketsBackInOrder(t *testing.T) {
	var a frameAssembler
	k := key(7)
	p := vp8Packets(k, 65534, 9000, 4) // across the sequence number wrap
	out, lost := feed(&a, p[2], p[0], p[3], p[1])
	if lost || len(out) != 1 || !bytes.Equal(out[0].frame, k) {
		t.Fatalf("got %v (lost %v), want the frame reassembled in order", out, lost)
	}
	// Stragglers from a settled frame change nothing.
	if out, lost := feed(&a, p[1]); len(out) != 0 || lost {
		t.Fatalf("a duplicate packet produced %v, lost %v", out, lost)
	}
}

func TestAssemblerWithholdsInterFramesAfterALoss(t *testing.T) {
	var a frameAssembler
	feed(&a, vp8Packets(key(1), 10, 1000, 2)...)

	damaged := vp8Packets(inter(2), 12, 2000, 3)
	out, lost := feed(&a, cat(
		[]*rtp.Packet{damaged[0], damaged[2]}, // the middle packet is lost
		vp8Packets(inter(3), 15, 3000, 2),
	)...)
	if len(out) != 0 || !lost {
		t.Fatalf("after a lost packet: got %v, lost %v; want nothing until a key frame", out, lost)
	}
	k := key(4)
	out, _ = feed(&a, vp8Packets(k, 17, 4000, 2)...)
	if len(out) != 1 || !bytes.Equal(out[0].frame, k) {
		t.Fatalf("got %v, want the key frame that resynchronises", out)
	}
}

func TestAssemblerNoticesAWholeFrameLost(t *testing.T) {
	var a frameAssembler
	feed(&a, vp8Packets(key(1), 10, 1000, 2)...)
	// Sequence numbers 12 and 13, a whole frame, never arrive.
	out, lost := feed(&a, vp8Packets(inter(3), 14, 3000, 2)...)
	if len(out) != 0 || !lost {
		t.Fatalf("after a frame lost entirely: got %v, lost %v; want the next inter frame withheld", out, lost)
	}
}

func TestAssemblerIgnoresOldFrames(t *testing.T) {
	var a frameAssembler
	feed(&a, vp8Packets(key(1), 10, 5000, 2)...)
	if out, lost := feed(&a, vp8Packets(key(2), 5, 1000, 2)...); len(out) != 0 || lost {
		t.Fatalf("a frame older than the last delivered produced %v, lost %v", out, lost)
	}
}

// Two real sessions, as a viewer and an agent: the viewer offers with
// ReceiveVideo, the agent sends frames on its track, and the viewer gets them
// back whole, then asks for a key frame and the agent hears it.
func TestReceiveVideoGetsTheAgentsFrames(t *testing.T) {
	agent, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close() }()
	if err := agent.CreateVideoTrack("barahn-screen", "screen"); err != nil {
		t.Fatal(err)
	}
	var keyFrameAsked atomic.Int32
	agent.OnKeyFrameRequest(func() { keyFrameAsked.Add(1) })

	viewer, err := NewPeerSession(loopbackConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = viewer.Close() }()
	if err := viewer.RequestKeyFrame(); err == nil {
		t.Error("RequestKeyFrame with no video coming succeeded")
	}
	if err := viewer.ReceiveVideo(); err != nil {
		t.Fatal(err)
	}
	frames := make(chan delivered, 64)
	viewer.OnVideoFrame(func(frame []byte, key bool) { frames <- delivered{frame, key} })

	agentCands := newCandidateRelay(agent)
	viewerCands := newCandidateRelay(viewer)
	agent.OnICECandidate(viewerCands.accept)
	viewer.OnICECandidate(agentCands.accept)
	offer, err := viewer.CreateOffer()
	if err != nil {
		t.Fatal(err)
	}
	answer, err := agent.CreateAnswer(offer)
	if err != nil {
		t.Fatal(err)
	}
	agentCands.ready()
	if err := viewer.SetRemoteAnswer(answer); err != nil {
		t.Fatal(err)
	}
	viewerCands.ready()

	// A real key frame, and an "inter" frame made from it by flipping the
	// frame-type bit: the receiver reassembles bytes, it does not decode.
	kf := vp8KeyFrameForTest(t)
	in := append([]byte(nil), kf...)
	in[0] |= 1

	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	sent := false
	for !sent {
		select {
		case <-deadline:
			t.Fatalf("never connected (agent %v, viewer %v)", agent.ConnectionState(), viewer.ConnectionState())
		case <-tick.C:
			if agent.IsConnected() {
				sent = true
			}
		}
	}
	// The track's first packets can be lost while DTLS settles, so keep
	// sending the key frame until one arrives.
	var first delivered
	for first.frame == nil {
		if err := agent.WriteVideoSample(kf, 33*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		select {
		case first = <-frames:
		case <-tick.C:
		case <-deadline:
			t.Fatal("no frame reached the viewer")
		}
	}
	if !first.key || !bytes.Equal(first.frame, kf) {
		t.Fatalf("first frame: key %v, %d bytes; want the %d-byte key frame intact", first.key, len(first.frame), len(kf))
	}

	if err := agent.WriteVideoSample(in, 33*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case f := <-frames:
			if f.key {
				continue // a key frame still in flight from the loop above
			}
			if !bytes.Equal(f.frame, in) {
				t.Fatalf("inter frame: %d bytes, want %d intact", len(f.frame), len(in))
			}
		case <-deadline:
			t.Fatal("the inter frame did not arrive")
		}
		break
	}

	if err := viewer.RequestKeyFrame(); err != nil {
		t.Fatal(err)
	}
	for keyFrameAsked.Load() == 0 {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("the agent never heard the viewer's key frame request")
		}
	}
}
