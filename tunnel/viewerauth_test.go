// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/webrtc"
	"github.com/gorilla/websocket"
)

func viewerKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// signedViewerMessage builds a viewer message the way a viewer would send it:
// signed for target, with the server's viewer_id added afterwards, outside the
// signature.
func signedViewerMessage(t *testing.T, priv ed25519.PrivateKey, m webrtc.SignalMessage, target string, now time.Time) []byte {
	t.Helper()
	m.TargetID = target
	if err := m.Sign(priv, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["viewer_id"] = "viewer-a"
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func trustOnly(pub ed25519.PublicKey) func(ed25519.PublicKey) bool {
	return func(k ed25519.PublicKey) bool { return bytes.Equal(k, pub) }
}

func TestViewerAuth_Check(t *testing.T) {
	pub, priv := viewerKey(t)
	_, strangerPriv := viewerKey(t)
	now := time.Unix(1_790_000_000, 0)
	offer := webrtc.SignalMessage{Type: webrtc.SignalOffer, SessionID: "s", SDP: "v=0\r\n"}

	t.Run("trusted and targeted is accepted once", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := signedViewerMessage(t, priv, offer, "agent-1", now)
		if err := a.check(raw, "agent-1", now); err != nil {
			t.Fatalf("first delivery: %v", err)
		}
		if err := a.check(raw, "agent-1", now.Add(time.Second)); !errors.Is(err, ErrViewerReplay) {
			t.Fatalf("second delivery: want ErrViewerReplay, got %v", err)
		}
	})

	t.Run("unsigned is refused", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := []byte(`{"type":"offer","sdp":"v=0","viewer_id":"viewer-a"}`)
		if err := a.check(raw, "agent-1", now); !errors.Is(err, webrtc.ErrSignalUnsigned) {
			t.Fatalf("want ErrSignalUnsigned, got %v", err)
		}
	})

	t.Run("a stranger's key is refused", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := signedViewerMessage(t, strangerPriv, offer, "agent-1", now)
		if err := a.check(raw, "agent-1", now); !errors.Is(err, ErrViewerUntrusted) {
			t.Fatalf("want ErrViewerUntrusted, got %v", err)
		}
	})

	t.Run("signed for another agent is refused", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := signedViewerMessage(t, priv, offer, "agent-2", now)
		if err := a.check(raw, "agent-1", now); !errors.Is(err, ErrViewerWrongTarget) {
			t.Fatalf("want ErrViewerWrongTarget, got %v", err)
		}
	})

	t.Run("a tampered SDP is refused", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := signedViewerMessage(t, priv, offer, "agent-1", now)
		raw = bytes.Replace(raw, []byte(`v=0`), []byte(`v=1`), 1)
		if err := a.check(raw, "agent-1", now); !errors.Is(err, webrtc.ErrSignalBadSig) {
			t.Fatalf("want ErrSignalBadSig, got %v", err)
		}
	})

	t.Run("expired is refused", func(t *testing.T) {
		a := &viewerAuth{trust: trustOnly(pub), nonces: webrtc.NewNonceCache()}
		raw := signedViewerMessage(t, priv, offer, "agent-1", now)
		if err := a.check(raw, "agent-1", now.Add(2*time.Minute)); !errors.Is(err, webrtc.ErrSignalExpired) {
			t.Fatalf("want ErrSignalExpired, got %v", err)
		}
	})

	// A refused message must not consume its nonce: otherwise anyone who can
	// write to the socket could burn a trusted viewer's nonces in advance.
	t.Run("a refused message does not burn its nonce", func(t *testing.T) {
		calls := 0
		a := &viewerAuth{
			trust: func(k ed25519.PublicKey) bool {
				calls++
				return calls > 1 && bytes.Equal(k, pub)
			},
			nonces: webrtc.NewNonceCache(),
		}
		raw := signedViewerMessage(t, priv, offer, "agent-1", now)
		if err := a.check(raw, "agent-1", now); !errors.Is(err, ErrViewerUntrusted) {
			t.Fatalf("first: want ErrViewerUntrusted, got %v", err)
		}
		if err := a.check(raw, "agent-1", now); err != nil {
			t.Fatalf("once trusted, the same message should pass: %v", err)
		}
	})
}

// firstReply serves one WebSocket, hands it to runSignalingLoop, sends msg to
// the agent and returns the first message the agent writes back within wait,
// or nil if none arrives. The agent_ready it sends on every connection is not
// a reply and is skipped.
func firstReply(t *testing.T, r *AgentStreamRunner, msg []byte, wait time.Duration) []byte {
	t.Helper()
	replied := make(chan []byte, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(wait))
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if bytes.Contains(data, []byte(`"agent_ready"`)) {
				continue
			}
			replied <- data
			return
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		r.runSignalingLoop(ctx, ws, nil)
		close(done)
	}()

	var got []byte
	select {
	case got = <-replied:
	case <-time.After(wait + time.Second):
	}
	cancel()
	_ = ws.Close()
	<-done
	return got
}

// runLoopAgainst reports whether the agent replied to msg at all. A
// session_start makes the agent either stream frames or announce that no
// capture backend exists, so any reply means the session was started.
func runLoopAgainst(t *testing.T, r *AgentStreamRunner, msg []byte, wait time.Duration) bool {
	t.Helper()
	return firstReply(t, r, msg, wait) != nil
}

// End to end through the signalling loop: with signed viewers required, a
// session_start the control plane forged starts nothing, and one a trusted
// viewer signed does.
func TestRequireSignedViewers_GatesSessionStart(t *testing.T) {
	pub, priv := viewerKey(t)
	creds := &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}

	forged := []byte(`{"type":"session_start","viewer_id":"viewer-a"}`)
	r := &AgentStreamRunner{creds: creds}
	r.RequireSignedViewers(trustOnly(pub))
	if runLoopAgainst(t, r, forged, 500*time.Millisecond) {
		t.Fatal("unsigned session_start started a session")
	}

	signed := signedViewerMessage(t, priv, webrtc.SignalMessage{Type: "session_start"}, "agent-1", time.Now())
	r = &AgentStreamRunner{creds: creds}
	r.RequireSignedViewers(trustOnly(pub))
	if !runLoopAgainst(t, r, signed, 5*time.Second) {
		t.Fatal("signed session_start from a trusted viewer started nothing")
	}
}

// Without the requirement, the loop behaves as before: an unsigned
// session_start is acted on.
func TestRequireSignedViewers_OffByDefault(t *testing.T) {
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	if !runLoopAgainst(t, r, []byte(`{"type":"session_start","viewer_id":"viewer-a"}`), 5*time.Second) {
		t.Fatal("without RequireSignedViewers, an unsigned session_start should still start a session")
	}
}
