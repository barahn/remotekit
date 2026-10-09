// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"encoding/json"
	"testing"
	"time"
)

// sink records what the gate writes to the socket.
type sink struct{ msgs []map[string]interface{} }

func (s *sink) write(data []byte) error {
	var m map[string]interface{}
	_ = json.Unmarshal(data, &m)
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *sink) types() []string {
	var out []string
	for _, m := range s.msgs {
		t, _ := m["type"].(string)
		if a, ok := m["active"].(bool); ok {
			t += map[bool]string{true: ":on", false: ":off"}[a]
		}
		out = append(out, t)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Entering relay mode is announced once, not per frame; leaving it is
// announced too; and the consumer hears each transition once.
func TestRelayGate_AnnouncesTransitions(t *testing.T) {
	var changes []bool
	g := &relayGate{onChange: func(a bool) { changes = append(changes, a) }}
	var s sink

	for i := 0; i < 3; i++ {
		if !g.relay(s.write) {
			t.Fatal("relay refused with the fallback enabled")
		}
	}
	g.direct(s.write)
	g.direct(s.write) // already direct: says nothing
	g.relay(s.write)

	if want := []string{"relay_mode:on", "relay_mode:off", "relay_mode:on"}; !sameStrings(s.types(), want) {
		t.Fatalf("socket got %v, want %v", s.types(), want)
	}
	if want := []bool{true, false, true}; len(changes) != 3 || changes[0] != want[0] || changes[1] != want[1] || changes[2] != want[2] {
		t.Fatalf("OnRelayMode got %v, want %v", changes, want)
	}
	if reason, _ := s.msgs[0]["reason"].(string); reason == "" {
		t.Fatal("the relay notice carries no reason for the viewer to show")
	}
}

// A viewer joining mid-relay did not see the notice, so it is sent again;
// the consumer, which already knows, is not told twice.
func TestRelayGate_ReannouncesToANewViewer(t *testing.T) {
	calls := 0
	g := &relayGate{onChange: func(bool) { calls++ }}
	var s sink

	g.relay(s.write)
	g.viewerJoined()
	g.relay(s.write)

	if want := []string{"relay_mode:on", "relay_mode:on"}; !sameStrings(s.types(), want) {
		t.Fatalf("socket got %v, want %v", s.types(), want)
	}
	if calls != 1 {
		t.Fatalf("OnRelayMode called %d times, want 1", calls)
	}
}

// With the fallback disabled no frame is allowed, the viewer is told why
// once, and relay mode is never entered.
func TestRelayGate_Disabled(t *testing.T) {
	calls := 0
	g := &relayGate{disabled: true, onChange: func(bool) { calls++ }}
	var s sink

	for i := 0; i < 3; i++ {
		if g.relay(s.write) {
			t.Fatal("a JPEG frame was allowed with the fallback disabled")
		}
	}
	if want := []string{relayRefusedNotice}; !sameStrings(s.types(), want) {
		t.Fatalf("socket got %v, want %v", s.types(), want)
	}
	if calls != 0 {
		t.Fatalf("OnRelayMode called %d times with the fallback disabled", calls)
	}
}

// Ending the session ends relay mode for the consumer without a socket
// notice, since the socket is going away.
func TestRelayGate_Stop(t *testing.T) {
	var changes []bool
	g := &relayGate{onChange: func(a bool) { changes = append(changes, a) }}
	var s sink
	g.relay(s.write)
	g.stop()
	if len(s.msgs) != 1 {
		t.Fatalf("stop wrote to the socket: %v", s.types())
	}
	if len(changes) != 2 || changes[1] {
		t.Fatalf("OnRelayMode got %v, want [true false]", changes)
	}
}

// Against the real loop: whatever the agent sends first after a session
// starts without WebRTC, it is never a JPEG frame. Where capture works (CI
// runs under Xvfb) that first message is the relay notice; where it does not,
// it is capture_unavailable, and there is no frame to precede.
func TestRelayMode_NoticeBeforeFirstFrame(t *testing.T) {
	r := &Runner{id: "agent-1"}
	switch got := replyType(t, firstReply(t, r, sessionStart, 5*time.Second)); got {
	case relayNotice, "capture_unavailable":
	default:
		t.Fatalf("first message was %q: a JPEG frame left before relay mode was announced", got)
	}
}

func TestRelayMode_DisabledSendsNoFrame(t *testing.T) {
	r := &Runner{id: "agent-1"}
	r.DisableRelayFallback(true)
	switch got := replyType(t, firstReply(t, r, sessionStart, 5*time.Second)); got {
	case relayRefusedNotice, "capture_unavailable":
	default:
		t.Fatalf("first message was %q with the fallback disabled", got)
	}
}
