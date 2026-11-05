// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/screen"
)

// signedSession runs a Runner and a Viewer that sign for each other over an
// in-memory pipe, each on its own clock. It returns whether the viewer has
// applied an answer, and the viewer's notices.
func signedSession(t *testing.T, agentClock, viewerClock func() time.Time) (func() bool, <-chan Notice) {
	t.Helper()
	agentPub, agentPriv, _ := ed25519.GenerateKey(rand.Reader)
	viewerPub, viewerPriv, _ := ed25519.GenerateKey(rand.Reader)

	r := New(Config{ID: "agent-1", SigningKey: agentPriv, Clock: agentClock})
	r.clip = clipboard.NewMemoryClipboard()
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	r.RequireSignedViewers(trustOnly(viewerPub))

	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1", SigningKey: viewerPriv, AgentKey: agentPub, Clock: viewerClock})
	notices := make(chan Notice, 16)
	v.OnNotice(func(n Notice) {
		select {
		case notices <- n:
		default:
		}
	})

	agentEnd, viewerEnd := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = r.Serve(ctx, agentEnd) }()
	go func() { defer wg.Done(); _ = v.Serve(ctx, viewerEnd) }()
	t.Cleanup(func() {
		cancel()
		_ = agentEnd.Close()
		wg.Wait()
	})
	return func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.answered
	}, notices
}

func within(d time.Duration, ok func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return ok()
}

// Signed signals are issued and checked on the clock each end is given. An
// agent on a clock 50s behind refuses an offer issued on the machine's own
// clock as not yet valid; given the same clock, the viewer's offer is
// answered. So two machines whose own clocks disagree negotiate once both
// are given one they trust, such as their server's (barahn/remotekit#179).
func TestSignedSignalsUseTheGivenClock(t *testing.T) {
	behind := func() time.Time { return time.Now().Add(-50 * time.Second) }
	answered, _ := signedSession(t, behind, time.Now)
	if within(2*time.Second, answered) {
		t.Fatal("the agent, 50s behind on its clock, answered an offer issued in its future")
	}
	answered, _ = signedSession(t, behind, behind)
	if !within(5*time.Second, answered) {
		t.Fatal("on one clock, 50s off the machines', the viewer never got an answer")
	}
}

// A viewer whose clock is behind by more than the signals allow drops the
// agent's answers as not yet valid. It says so, with how far off they were,
// instead of waiting without a word (barahn/chirp#191).
func TestAnAnswerFromAClockAheadIsReported(t *testing.T) {
	behind := func() time.Time { return time.Now().Add(-50 * time.Second) }
	answered, notices := signedSession(t, time.Now, behind)
	if within(2*time.Second, answered) {
		t.Fatal("the viewer took an answer issued 50s in its future")
	}
	for {
		select {
		case n := <-notices:
			if n.Type != NoticeClockOff {
				continue
			}
			off, err := time.ParseDuration(n.Detail)
			if err != nil || off < 45*time.Second || off > 55*time.Second {
				t.Fatalf("told the agent's clock is %q off, want about 50s ahead", n.Detail)
			}
			return
		default:
			t.Fatal("the viewer dropped the answers without a notice")
		}
	}
}
