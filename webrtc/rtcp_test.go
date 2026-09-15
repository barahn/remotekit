package webrtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
)

// newDispatchProbe returns a PeerSession wired to count key-frame requests,
// without a PeerConnection: dispatchRTCP is deliberately separable from the
// read loop so the decision can be tested without a live connection and the
// network flakiness that would come with one.
func newDispatchProbe(t *testing.T, interval time.Duration) (*PeerSession, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ps := &PeerSession{keyFrameRequestInterval: interval}
	ps.OnKeyFrameRequest(func() { calls.Add(1) })
	return ps, &calls
}

func TestDispatchRTCPRequestsKeyFrame(t *testing.T) {
	tests := []struct {
		name    string
		packets []rtcp.Packet
		want    int32
	}{
		{
			name:    "picture loss indication",
			packets: []rtcp.Packet{&rtcp.PictureLossIndication{}},
			want:    1,
		},
		{
			name:    "full intra request",
			packets: []rtcp.Packet{&rtcp.FullIntraRequest{}},
			want:    1,
		},
		{
			name: "a receiver report on its own asks for nothing",
			packets: []rtcp.Packet{
				&rtcp.ReceiverReport{},
				&rtcp.SenderReport{},
			},
			want: 0,
		},
		{
			name: "a request buried in a compound packet still counts",
			packets: []rtcp.Packet{
				&rtcp.ReceiverReport{},
				&rtcp.PictureLossIndication{},
				&rtcp.SourceDescription{},
			},
			want: 1,
		},
		{
			name: "two requests in one batch are one request",
			packets: []rtcp.Packet{
				&rtcp.PictureLossIndication{},
				&rtcp.FullIntraRequest{},
			},
			want: 1,
		},
		{
			name:    "an empty batch does nothing",
			packets: nil,
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ps, calls := newDispatchProbe(t, time.Hour)
			ps.dispatchRTCP(tc.packets)
			if got := calls.Load(); got != tc.want {
				t.Errorf("callback ran %d times, want %d", got, tc.want)
			}
		})
	}
}

// TestDispatchRTCPCoalescesBursts covers the behaviour that makes this safe to
// act on at all.
//
// A receiver that has lost a frame does not ask once and wait — it repeats the
// request until it sees an intra frame. Honouring each one would encode a burst
// of full refreshes, the most expensive frame there is, precisely when the
// connection is already in trouble.
func TestDispatchRTCPCoalescesBursts(t *testing.T) {
	const interval = 60 * time.Millisecond
	ps, calls := newDispatchProbe(t, interval)

	burst := []rtcp.Packet{&rtcp.PictureLossIndication{}}
	for i := 0; i < 10; i++ {
		ps.dispatchRTCP(burst)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a burst of 10 requests produced %d key frames, want 1", got)
	}

	time.Sleep(interval + 20*time.Millisecond)
	ps.dispatchRTCP(burst)
	if got := calls.Load(); got != 2 {
		t.Errorf("after the interval elapsed the next request produced %d key frames in total, want 2", got)
	}
}

// TestDispatchRTCPWithoutCallback guards the case where nobody has registered
// an interest: the agent negotiates a video track before anything is wired to
// the encoder, so requests arriving in that window must be dropped rather than
// panic.
func TestDispatchRTCPWithoutCallback(t *testing.T) {
	ps := &PeerSession{}
	ps.dispatchRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{}})
}

// TestKeyFrameRequestIntervalDefault checks that a zero interval means the
// default rather than "no coalescing at all", which would let a burst through.
func TestKeyFrameRequestIntervalDefault(t *testing.T) {
	ps, calls := newDispatchProbe(t, 0)
	ps.SetKeyFrameRequestInterval(0)

	burst := []rtcp.Packet{&rtcp.PictureLossIndication{}}
	for i := 0; i < 5; i++ {
		ps.dispatchRTCP(burst)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("with the default interval a burst produced %d key frames, want 1", got)
	}
}
