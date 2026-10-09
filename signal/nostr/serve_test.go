// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/barahn/remotekit/stream"
	"github.com/barahn/remotekit/webrtc"
)

// Conn is a stream.SignalConn.
var _ stream.SignalConn = (*Conn)(nil)

// signedOffer builds a viewer's offer signed with its device key for agentID.
func signedOffer(t *testing.T, priv ed25519.PrivateKey, agentID, sdp string) []byte {
	t.Helper()
	m := webrtc.SignalMessage{Type: webrtc.SignalOffer, SessionID: agentID, SDP: sdp, TargetID: agentID}
	if err := m.Sign(priv, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A whole negotiation runs over relays: the agent serves a Listen conn, a
// viewer dials it with a signed offer, and gets back an answer its own peer
// accepts. An unsigned offer, meanwhile, gets nothing -- the relays are as
// untrusted as the control plane, and the runner's checks are what stop them.
func TestServeNegotiatesOverRelays(t *testing.T) {
	relay := newFakeRelay(t)
	cfg := Config{Relays: []string{relay.url()}}

	viewerPub, viewerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const agentID = "agent-nostr"
	runner := stream.New(stream.Config{ID: agentID})
	runner.RequireSignedViewers(func(k ed25519.PublicKey) bool { return bytes.Equal(k, viewerPub) })
	runner.DisableRelayFallback(true)

	agentConn, err := Listen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- runner.Serve(ctx, agentConn) }()

	viewer := dial(t, cfg, agentConn.PublicKey())

	// Unsigned: refused before anything is negotiated.
	if err := viewer.WriteMessage([]byte(`{"type":"offer","sdp":"v=0"}`)); err != nil {
		t.Fatal(err)
	}
	nothingWithin(t, viewer, 500*time.Millisecond)

	peer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	if err := peer.CreateVideoTrack("viewer", "v"); err != nil {
		t.Fatal(err)
	}
	offer, err := peer.CreateOffer()
	if err != nil {
		t.Fatal(err)
	}
	if err := viewer.WriteMessage(signedOffer(t, viewerPriv, agentID, offer)); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		ch := make(chan []byte, 1)
		go func() {
			if m, err := viewer.ReadMessage(); err == nil {
				ch <- m
			}
		}()
		select {
		case m := <-ch:
			var sm webrtc.SignalMessage
			if json.Unmarshal(m, &sm) != nil || sm.Type != webrtc.SignalAnswer {
				continue // candidates, notices
			}
			if err := peer.SetRemoteAnswer(sm.SDP); err != nil {
				t.Fatalf("answer over relays was not accepted: %v", err)
			}
			cancel()
			select {
			case <-served:
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not return after cancel")
			}
			return
		case <-deadline:
			t.Fatal("no answer arrived over the relays")
		}
	}
}
