// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/webrtc"
)

// An on-demand session, as a consumer with no enrolment runs it: the runner is
// built from an ID and a key held in memory -- no credentials file, no key
// file -- served over an in-memory transport, and goes nowhere until the
// person at the machine consents. Once they do, the viewer gets an answer it
// can verify against the runner's key.
func TestNew_ServesAnOnDemandSessionThroughConsent(t *testing.T) {
	agentPub, agentPriv := viewerKey(t)
	viewerPub, viewerPriv := viewerKey(t)
	const id = "on-demand-1"

	r := New(Config{ID: id, SigningKey: agentPriv})
	r.RequireScreenViewConsent(true)
	r.RequireSignedViewers(trustOnly(viewerPub))
	r.DisableRelayFallback(true)
	if r.ID() != id {
		t.Fatalf("ID() = %q, want %q", r.ID(), id)
	}

	peer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	if err := peer.CreateVideoTrack("viewer", "v"); err != nil {
		t.Fatal(err)
	}
	sdp, err := peer.CreateOffer()
	if err != nil {
		t.Fatal(err)
	}
	offer := func() []byte {
		return signedViewerMessage(t, viewerPriv, webrtc.SignalMessage{Type: webrtc.SignalOffer, SessionID: id, SDP: sdp}, id, time.Now())
	}

	agent, viewer := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Serve(ctx, agent) }()

	// Before consent: the viewer is told why, and nothing is negotiated.
	if err := viewer.WriteMessage(offer()); err != nil {
		t.Fatal(err)
	}
	if got := replyType(t, viewer.next(5*time.Second)); got != "consent_required" {
		t.Fatalf("offer before consent: got %q, want consent_required", got)
	}

	r.Grant(PermissionScreenView)
	if err := viewer.WriteMessage(offer()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data := viewer.next(time.Until(deadline))
		if data == nil {
			break
		}
		var head struct{ Type string }
		if json.Unmarshal(data, &head) != nil || head.Type != string(webrtc.SignalAnswer) {
			continue
		}
		m, err := webrtc.DecodeSignalMessage(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.VerifyFrom(agentPub, time.Now()); err != nil {
			t.Fatalf("answer does not verify against the runner's in-memory key: %v", err)
		}
		if m.SessionID != id {
			t.Fatalf("answer for session %q, want %q", m.SessionID, id)
		}
		return
	}
	t.Fatal("no answer after screen_view was granted")
}

// The package exists so a consumer can serve a session without the reverse
// tunnel: it must never pull in tunnel, or yamux with it.
func TestDepsExcludeTunnelAndYamux(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not on PATH: %v", err)
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/barahn/remotekit/tunnel" || strings.HasPrefix(dep, "github.com/hashicorp/yamux") {
			t.Errorf("stream depends on %s", dep)
		}
	}
}
