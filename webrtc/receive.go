// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"errors"
	"fmt"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

// The receive side of a PeerSession: what a viewer written in Go uses to show
// the screen an agent sends (barahn/remotekit#87). The agent never calls any
// of it.
//
// Frames are delivered whole and only in a sequence a decoder can follow. VP8
// inter frames predict from the frames before them, so after a lost packet
// every frame up to the next key frame would decode to garbage; they are
// withheld instead, and a picture loss indication asks the sender for a key
// frame at once rather than at its next periodic one.

// ReceiveVideo makes this session ask for the other side's video. It is for
// the side that makes the offer, and must be called before CreateOffer so the
// offer carries a receive-only VP8 m-line, exactly as the browser viewer does.
func (ps *PeerSession) ReceiveVideo() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return errors.New("webrtc: peer session closed")
	}
	if _, err := ps.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return fmt.Errorf("webrtc: failed to add a video receiver: %w", err)
	}
	return nil
}

// OnVideoFrame registers fn for every VP8 frame received, whole, with whether
// it is a key frame. The first frame fn sees is always a key frame, and so is
// the first after any loss: fn can hand every frame straight to a decoder.
//
// Register it before the negotiation that brings the track up. It runs on the
// track's reader goroutine, one frame at a time; a slow fn delays the frames
// behind it, and pion drops packets once its buffer fills, which costs a key
// frame. frame is fn's to keep.
func (ps *PeerSession) OnVideoFrame(fn func(frame []byte, keyFrame bool)) {
	ps.rxMu.Lock()
	defer ps.rxMu.Unlock()
	ps.onVideoFrame = fn
}

// RequestKeyFrame asks the sender for a key frame, as a decoder that has lost
// its way does. Requests closer together than the key-frame request interval
// (SetKeyFrameRequestInterval) are dropped here, since the sender would absorb
// them anyway. It returns an error if no video is being received.
func (ps *PeerSession) RequestKeyFrame() error {
	ps.mu.RLock()
	interval := ps.keyFrameRequestInterval
	pc := ps.pc
	ps.mu.RUnlock()
	if interval <= 0 {
		interval = defaultKeyFrameRequestInterval
	}

	ps.rxMu.Lock()
	ssrc, ok := ps.remoteVideoSSRC, ps.receivingVideo
	now := time.Now()
	tooSoon := !ps.lastKeyFrameAsked.IsZero() && now.Sub(ps.lastKeyFrameAsked) < interval
	if ok && !tooSoon {
		ps.lastKeyFrameAsked = now
	}
	ps.rxMu.Unlock()

	if !ok {
		return errors.New("webrtc: no video is being received")
	}
	if tooSoon {
		return nil
	}
	return pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

// acceptTrack reads a remote video track for as long as it lives, and hands
// its frames to OnVideoFrame's callback.
func (ps *PeerSession) acceptTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if track.Kind() != webrtc.RTPCodecTypeVideo || track.Codec().MimeType != webrtc.MimeTypeVP8 {
		return
	}
	ps.rxMu.Lock()
	ps.remoteVideoSSRC = uint32(track.SSRC())
	ps.receivingVideo = true
	ps.rxMu.Unlock()

	var a frameAssembler
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return // the track or the session ended
		}
		frame, key, ok := a.push(pkt)
		if a.lost {
			_ = ps.RequestKeyFrame()
		}
		if !ok {
			continue
		}
		ps.rxMu.Lock()
		fn := ps.onVideoFrame
		ps.rxMu.Unlock()
		if fn != nil {
			fn(frame, key)
		}
	}
}

// frameAssembler rebuilds VP8 frames from RTP packets (RFC 7741). A frame is
// the packets sharing one timestamp, from the one that starts partition 0 to
// the one carrying the marker bit, with no sequence number missing between.
//
// It completes a frame on its marker, not on the arrival of the next frame:
// the sender skips frames when the screen does not change, so waiting for the
// next one would hold a keystroke's echo back until something else moved.
// Packets reordered within a frame are put back in order; a packet missing at
// the marker, or a frame that never completes before the next begins, is a
// loss.
type frameAssembler struct {
	// waitKey is set from the start and after any loss: nothing is delivered
	// until a key frame. lost is set by the push that found the loss, so the
	// caller can ask for one.
	waitKey bool
	started bool
	lost    bool

	// The frame being assembled: its timestamp, and its payloads by sequence
	// number. first is its first packet's sequence number once seen, and
	// end its marker packet's.
	ts       uint32
	parts    map[uint16][]byte
	first    uint16
	hasFirst bool
	end      uint16
	hasEnd   bool

	// nextSeq is the sequence number expected after the last whole frame,
	// so a frame lost entirely is noticed too.
	nextSeq uint16
	hasNext bool
	// done is the timestamp of the last frame completed or given up on;
	// stragglers for it are ignored.
	done    uint32
	hasDone bool
}

// maxFrameParts bounds one frame's packets. A 4K key frame at high quality is
// a few hundred; anything near this is not a frame worth waiting for.
const maxFrameParts = 4096

// push takes one packet and returns a frame when it completes one that may be
// delivered.
func (a *frameAssembler) push(pkt *rtp.Packet) (frame []byte, keyFrame, ok bool) {
	if !a.started {
		a.started, a.waitKey = true, true
	}
	a.lost = false

	if a.hasDone && pkt.Timestamp == a.done {
		return nil, false, false // a straggler from a frame already settled
	}
	if a.hasDone && tsBefore(pkt.Timestamp, a.done) {
		return nil, false, false
	}
	if a.parts != nil && pkt.Timestamp != a.ts {
		if tsBefore(pkt.Timestamp, a.ts) {
			return nil, false, false
		}
		// The next frame began before this one completed.
		a.giveUp()
	}
	if a.parts == nil {
		a.ts = pkt.Timestamp
		a.parts = make(map[uint16][]byte)
		a.hasFirst, a.hasEnd = false, false
	}

	var vp8 codecs.VP8Packet
	payload, err := vp8.Unmarshal(pkt.Payload)
	if err != nil || len(a.parts) >= maxFrameParts {
		a.giveUp()
		return nil, false, false
	}
	a.parts[pkt.SequenceNumber] = payload
	if vp8.S == 1 && vp8.PID == 0 {
		a.first, a.hasFirst = pkt.SequenceNumber, true
	}
	if pkt.Marker {
		a.end, a.hasEnd = pkt.SequenceNumber, true
	}
	if !a.hasFirst || !a.hasEnd {
		return nil, false, false
	}

	n := int(a.end-a.first) + 1
	if n > len(a.parts) {
		return nil, false, false // still waiting for a reordered packet
	}
	if n != len(a.parts) {
		a.giveUp() // packets outside the frame's bounds: not a frame
		return nil, false, false
	}
	gap := a.hasNext && a.first != a.nextSeq
	// Sequence numbers wrap, so the walk is in uint16 and stops after end.
	for seq := a.first; ; seq++ {
		frame = append(frame, a.parts[seq]...)
		if seq == a.end {
			break
		}
	}
	a.nextSeq, a.hasNext = a.end+1, true
	a.done, a.hasDone = a.ts, true
	a.parts = nil

	if gap {
		// A whole frame went missing before this one.
		a.waitKey, a.lost = true, true
	}
	keyFrame = len(frame) > 0 && frame[0]&1 == 0
	if a.waitKey && !keyFrame {
		a.lost = true
		return nil, false, false
	}
	a.waitKey = false
	return frame, keyFrame, true
}

// giveUp abandons the frame being assembled as lost.
func (a *frameAssembler) giveUp() {
	if a.parts != nil {
		a.done, a.hasDone = a.ts, true
	}
	a.parts = nil
	a.hasNext = false
	a.waitKey, a.lost = true, true
}

// tsBefore reports whether RTP timestamp x comes before y, allowing for
// wraparound.
func tsBefore(x, y uint32) bool { return x != y && y-x < 1<<31 }
