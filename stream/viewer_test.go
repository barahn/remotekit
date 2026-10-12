// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"image"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/webrtc"
)

// session runs a Runner and a Viewer against each other over an in-memory
// transport, and returns the viewer's decoded frames and notices.
func session(t *testing.T, r *Runner, v *Viewer) (<-chan image.Rectangle, <-chan Notice) {
	t.Helper()
	frames := make(chan image.Rectangle, 16)
	notices := make(chan Notice, 16)
	v.OnFrame(func(img *image.YCbCr) {
		select {
		case frames <- img.Rect:
		default:
		}
	})
	v.OnNotice(func(n Notice) { notices <- n })

	agentEnd, viewerEnd := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = r.Serve(ctx, agentEnd); done <- struct{}{} }()
	go func() { _ = v.Serve(ctx, viewerEnd); done <- struct{}{} }()
	t.Cleanup(func() {
		cancel()
		<-done
		<-done
	})
	return frames, notices
}

// firstFrame waits for the viewer's first decoded frame, skipping the test
// where the machine running it has no screen to capture.
func firstFrame(t *testing.T, frames <-chan image.Rectangle, notices <-chan Notice) image.Rectangle {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case rect := <-frames:
			return rect
		case n := <-notices:
			if n.Type == "capture_unavailable" {
				t.Skipf("no screen to capture here: %s", n.Detail)
			}
			t.Fatalf("the agent sent %+v instead of its screen", n)
		case <-deadline:
			t.Fatal("no frame decoded within 30s")
		}
	}
}

func TestViewerShowsTheRunnersScreen(t *testing.T) {
	r := New(Config{ID: "session-1", PeerConfig: &webrtc.PeerConfig{}})
	r.RequireScreenViewConsent(true)
	r.Grant(PermissionScreenView)
	v := NewViewer(ViewerConfig{ID: "viewer-a", AgentID: "session-1", PeerConfig: &webrtc.PeerConfig{}})

	frames, notices := session(t, r, v)
	rect := firstFrame(t, frames, notices)
	if rect.Dx() < 16 || rect.Dy() < 16 {
		t.Fatalf("decoded a %v picture", rect)
	}

	// The control channel is up once video flows. The agent refuses the
	// event itself -- remote control was not granted, so the test does not
	// move the real pointer -- but the viewer can send it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := v.SendInput(bark.InputEvent{Type: "mouse_move", X: 0.5, Y: 0.5})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SendInput: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Both ends sign and both verify: the runner takes only this viewer's offer,
// and the viewer takes only this runner's answer.
func TestViewerAndRunnerAuthenticateEachOther(t *testing.T) {
	agentPub, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	viewerPub, viewerKey, _ := ed25519.GenerateKey(rand.Reader)

	r := New(Config{ID: "session-2", SigningKey: agentKey, PeerConfig: &webrtc.PeerConfig{}})
	r.RequireSignedViewers(func(k ed25519.PublicKey) bool { return k.Equal(viewerPub) })
	r.RequireScreenViewConsent(true)
	r.Grant(PermissionScreenView)
	v := NewViewer(ViewerConfig{
		ID: "viewer-b", AgentID: "session-2",
		SigningKey: viewerKey, AgentKey: agentPub,
		PeerConfig: &webrtc.PeerConfig{},
	})
	frames, notices := session(t, r, v)
	firstFrame(t, frames, notices)
}

// A viewer expecting another agent's key ignores this one's answers, so no
// picture arrives.
func TestViewerRefusesAnAnswerFromTheWrongKey(t *testing.T) {
	_, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)

	r := New(Config{ID: "session-3", SigningKey: agentKey, PeerConfig: &webrtc.PeerConfig{}})
	r.Grant(PermissionScreenView)
	v := NewViewer(ViewerConfig{ID: "viewer-c", AgentID: "session-3", AgentKey: otherPub, PeerConfig: &webrtc.PeerConfig{}})
	frames, _ := session(t, r, v)
	select {
	case rect := <-frames:
		t.Fatalf("decoded a %v frame from an agent whose key is not trusted", rect)
	case <-time.After(3 * time.Second):
	}
}

func TestViewerIsToldWhenConsentIsMissing(t *testing.T) {
	r := New(Config{ID: "session-4", PeerConfig: &webrtc.PeerConfig{}})
	r.RequireScreenViewConsent(true)
	v := NewViewer(ViewerConfig{ID: "viewer-d", AgentID: "session-4", PeerConfig: &webrtc.PeerConfig{}})
	_, notices := session(t, r, v)
	select {
	case n := <-notices:
		if n.Type != "consent_required" || n.Detail != PermissionScreenView {
			t.Fatalf("notice %+v, want consent_required for %s", n, PermissionScreenView)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notice")
	}
}
