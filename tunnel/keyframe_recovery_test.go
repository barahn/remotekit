// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	pion "github.com/pion/webrtc/v4"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/screen/codec/vp8check"
	"github.com/barahn/remotekit/webrtc"
)

// This is the loop the viewer depends on when a connection drops data: the
// receiver cannot decode, asks for an intra frame over RTCP, and the encoder
// serves one. Every piece of it has a test of its own -- dispatchRTCP coalesces
// bursts, the media path carries samples, the encoder emits a key frame on
// demand -- and none of those says the pieces are wired to each other.
//
// It lives in pkg/tunnel because this is the only package that imports both
// pkg/screen and pkg/webrtc; pkg/webrtc deliberately does not depend on the
// screen package, which is what drives it.
const (
	recWidth  = 320
	recHeight = 240
	recFPS    = 30

	// Loss is introduced by discarding RTP packets as they arrive rather than
	// by impairing the link, which would need root to set up. What that costs
	// is fidelity about *how* packets go missing: there is no reordering, no
	// burst correlation, no congestion response. What it preserves is the part
	// under test -- the receiver holds an undecodable stream, and recovery has
	// to come from the key-frame request.
	recDropAfterFrames = 10
	recDropPackets     = 25
)

// recFrame is a source picture with enough movement that no two frames are
// alike and inter prediction still has something to work with.
func recFrame(idx int) []byte {
	buf := make([]byte, 0, recWidth*recHeight*3/2)
	for y := 0; y < recHeight; y++ {
		for x := 0; x < recWidth; x++ {
			v := (x/16+y/16)%2*80 + 60
			if bar := (idx * 5) % recWidth; x >= bar && x < bar+16 {
				v = 240
			}
			buf = append(buf, byte(v))
		}
	}
	for _, base := range []int{95, 165} {
		for y := 0; y < recHeight/2; y++ {
			for x := 0; x < recWidth/2; x++ {
				buf = append(buf, byte(base+(x/8+y/8+idx)%2*35))
			}
		}
	}
	return buf
}

// assembled is one VP8 frame rebuilt from its RTP packets.
type assembled struct {
	data    []byte
	partial bool // at least one of its packets was dropped
}

func (a assembled) isKeyFrame() bool { return len(a.data) > 0 && a.data[0]&1 == 0 }

// lossyReceiver stands in for the browser: it reassembles VP8 frames from RTP,
// throws a burst of packets away once the stream is running, and then asks for
// an intra frame the way a decoder that has lost its reference does.
type lossyReceiver struct {
	mu        sync.Mutex
	frames    []assembled
	current   []byte
	corrupt   bool
	dropped   int
	sentPLI   bool
	pliAt     int // index into frames at the moment the PLI went out
	pliSentCh chan struct{}
}

func newLossyReceiver() *lossyReceiver {
	return &lossyReceiver{pliSentCh: make(chan struct{})}
}

// consume feeds one RTP packet in, returning true when the caller should now
// send a picture-loss indication.
func (r *lossyReceiver) consume(payload []byte, marker bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Drop a burst once the stream is established, so there is a decodable
	// prefix before the damage and the loss lands mid-sequence rather than on
	// the first key frame.
	if len(r.frames) >= recDropAfterFrames && !r.sentPLI && r.dropped < recDropPackets {
		r.dropped++
		r.corrupt = true
		return false
	}

	vp8Pkt := &codecs.VP8Packet{}
	body, err := vp8Pkt.Unmarshal(payload)
	if err != nil {
		return false
	}
	r.current = append(r.current, body...)
	if !marker {
		return false
	}

	r.frames = append(r.frames, assembled{data: r.current, partial: r.corrupt})
	r.current = nil
	wasCorrupt := r.corrupt
	r.corrupt = false

	// The burst is spent and the frame carrying it is complete: this is the
	// point at which a real decoder gives up and asks for a fresh start.
	if wasCorrupt && r.dropped >= recDropPackets && !r.sentPLI {
		r.sentPLI = true
		r.pliAt = len(r.frames)
		close(r.pliSentCh)
		return true
	}
	return false
}

func (r *lossyReceiver) snapshot() ([]assembled, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]assembled, len(r.frames))
	copy(out, r.frames)
	return out, r.pliAt, r.sentPLI
}

// TestKeyFrameRequestRecoversTheStream loses a burst of RTP mid-stream, has the
// receiver ask for an intra frame, and checks that what arrives afterwards is a
// stream libvpx can decode from scratch.
//
// Losing packets is not enough on its own to prove anything: VP8 prediction is
// closed-loop, so a receiver that has missed a reference decodes everything
// after it wrongly and keeps doing so until an intra frame arrives. The
// assertion that matters is that one arrives, promptly, because the receiver
// asked.
func TestKeyFrameRequestRecoversTheStream(t *testing.T) {
	tool, required, err := vp8check.Availability()
	if err != nil {
		if required {
			t.Fatalf("%s is required (%s is set) but unavailable: %v",
				vp8check.ToolName, vp8check.RequiredEnv, err)
		}
		t.Skipf("skipping: %v (set %s=1 to make this fatal)", err, vp8check.RequiredEnv)
	}

	sender, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer func() { _ = sender.Close() }()
	if err := sender.CreateVideoTrack("barahn-screen", "screen"); err != nil {
		t.Fatalf("CreateVideoTrack: %v", err)
	}

	var keyFrameRequests atomic.Int32
	var wantKeyFrame atomic.Bool
	sender.OnKeyFrameRequest(func() {
		keyFrameRequests.Add(1)
		wantKeyFrame.Store(true)
	})

	receiver, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer receiver.Close() //nolint:errcheck // test teardown

	if _, err := receiver.AddTransceiverFromKind(pion.RTPCodecTypeVideo,
		pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("AddTransceiver: %v", err)
	}

	rec := newLossyReceiver()
	receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		if track.Kind() != pion.RTPCodecTypeVideo {
			return
		}
		ssrc := uint32(track.SSRC())
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}
			if rec.consume(pkt.Payload, pkt.Marker) {
				// A real browser sends these repeatedly until it sees an intra
				// frame; one is enough to show the loop closes, and the
				// coalescing of the rest is covered in pkg/webrtc.
				_ = receiver.WriteRTCP([]rtcp.Packet{
					&rtcp.PictureLossIndication{MediaSSRC: ssrc},
				})
			}
		}
	})

	connectPeers(t, sender, receiver)

	enc, err := vp8.NewEncoder(recWidth, recHeight, recFPS)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	// Long enough that the periodic refresh cannot be what rescues the stream:
	// if a key frame shows up soon after the PLI, the PLI is why.
	enc.SetKeyFrameInterval(600)

	stop := make(chan struct{})
	var sendErr atomic.Value
	go func() {
		tick := time.NewTicker(time.Second / recFPS)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if wantKeyFrame.Swap(false) {
				enc.ForceKeyFrame()
			}
			frame, encErr := enc.Encode(recFrame(i))
			if encErr != nil {
				sendErr.Store(encErr)
				return
			}
			if writeErr := sender.WriteVideoSample(frame, time.Second/recFPS); writeErr != nil {
				sendErr.Store(writeErr)
				return
			}
		}
	}()

	select {
	case <-rec.pliSentCh:
	case <-time.After(30 * time.Second):
		close(stop)
		t.Fatal("the receiver never lost enough to ask for a key frame")
	}

	// Give the request time to cross and the next frames time to arrive.
	deadline := time.After(10 * time.Second)
	for {
		frames, pliAt, _ := rec.snapshot()
		if recovered(frames, pliAt) {
			break
		}
		select {
		case <-deadline:
			close(stop)
			t.Fatalf("no key frame arrived in the %d frames after the request",
				len(frames)-pliAt)
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Let a few more frames land so the recovered stream has inter frames too.
	time.Sleep(500 * time.Millisecond)
	close(stop)

	if v := sendErr.Load(); v != nil {
		t.Fatalf("the send loop failed: %v", v)
	}
	if n := keyFrameRequests.Load(); n == 0 {
		t.Fatal("the sender never saw a key-frame request")
	}

	frames, pliAt, _ := rec.snapshot()
	key := firstKeyFrameAfter(frames, pliAt)
	t.Logf("%d frames received, burst of %d packets dropped, key frame %d frames after the request",
		len(frames), recDropPackets, key-pliAt)

	// Recovery has to be prompt to be worth anything. At 30fps a handful of
	// frames is a blink; the periodic refresh is 600 frames away.
	if gap := key - pliAt; gap > 30 {
		t.Errorf("the key frame took %d frames to arrive after the request", gap)
	}

	// The real assertion: from that key frame onward the receiver holds a
	// stream that decodes standalone. If the encoder had served a frame that
	// still referenced what was lost, this is where it would show.
	tail := make([][]byte, 0, len(frames)-key)
	for _, f := range frames[key:] {
		if f.partial {
			t.Fatalf("a frame after the key frame is missing packets; the drop window leaked")
		}
		tail = append(tail, f.data)
	}
	if len(tail) < 2 {
		t.Fatalf("only %d frames after recovery; not enough to check the stream", len(tail))
	}

	data, err := ivf.Marshal(ivf.Config{
		Width: recWidth, Height: recHeight,
		FPSNumerator: recFPS, FPSDenominator: 1,
	}, tail)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := vp8check.Decode(tool, data, recWidth, recHeight)
	if err != nil {
		t.Fatalf("the recovered stream does not decode: %v", err)
	}
	if len(decoded) != len(tail) {
		t.Fatalf("decoded %d of %d recovered frames", len(decoded), len(tail))
	}
}

func recovered(frames []assembled, pliAt int) bool {
	return firstKeyFrameAfter(frames, pliAt) >= 0
}

func firstKeyFrameAfter(frames []assembled, pliAt int) int {
	for i := pliAt; i < len(frames); i++ {
		if frames[i].isKeyFrame() && !frames[i].partial {
			return i
		}
	}
	return -1
}

// connectPeers negotiates the two ends the way pkg/tunnel/agent_stream.go does:
// the viewer offers, the agent answers, and candidates are trickled across
// because both descriptions are set long before ICE has gathered anything.
func connectPeers(t *testing.T, sender *webrtc.PeerSession, receiver *pion.PeerConnection) {
	t.Helper()

	var (
		mu            sync.Mutex
		pendingToSend []string
		pendingToRecv []pion.ICECandidateInit
		senderReady   bool
		receiverReady bool
	)

	sender.OnICECandidate(func(candidateJSON string) {
		var init pion.ICECandidateInit
		if err := unmarshalCandidate(candidateJSON, &init); err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !receiverReady {
			pendingToRecv = append(pendingToRecv, init)
			return
		}
		_ = receiver.AddICECandidate(init)
	})
	receiver.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		raw, err := marshalCandidate(c.ToJSON())
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !senderReady {
			pendingToSend = append(pendingToSend, raw)
			return
		}
		_ = sender.AddICECandidate(raw)
	})

	offer, err := receiver.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := receiver.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	answerSDP, err := sender.CreateAnswer(offer.SDP)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}

	mu.Lock()
	senderReady = true
	toSend := pendingToSend
	pendingToSend = nil
	mu.Unlock()
	for _, c := range toSend {
		_ = sender.AddICECandidate(c)
	}

	if err := receiver.SetRemoteDescription(pion.SessionDescription{
		Type: pion.SDPTypeAnswer, SDP: answerSDP,
	}); err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}

	mu.Lock()
	receiverReady = true
	toRecv := pendingToRecv
	pendingToRecv = nil
	mu.Unlock()
	for _, c := range toRecv {
		_ = receiver.AddICECandidate(c)
	}

	deadline := time.After(20 * time.Second)
	for !sender.IsConnected() {
		select {
		case <-deadline:
			t.Fatalf("the peers never connected (sender %v, receiver %v)",
				sender.ConnectionState(), receiver.ConnectionState())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func marshalCandidate(c pion.ICECandidateInit) (string, error) {
	b, err := json.Marshal(c)
	return string(b), err
}

func unmarshalCandidate(raw string, out *pion.ICECandidateInit) error {
	return json.Unmarshal([]byte(raw), out)
}
