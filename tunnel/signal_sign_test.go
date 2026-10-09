// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/webrtc"
)

func TestSignalSigningKey(t *testing.T) {
	if k, err := signalSigningKey(&AgentCredentials{AgentID: "a"}); k != nil || err != nil {
		t.Errorf("keyless agent: got key=%v err=%v, want nil, nil", k != nil, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")
	want, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signalSigningKey(&AgentCredentials{AgentID: "a", DeviceKeyPath: path})
	if err != nil || !got.Equal(want) {
		t.Errorf("keyed agent: got err=%v, same key=%v", err, got.Equal(want))
	}

	if _, err := signalSigningKey(&AgentCredentials{AgentID: "a", DeviceKeyPath: filepath.Join(dir, "missing.key")}); err == nil {
		t.Error("missing key file: want an error rather than signing nothing")
	}
}

// pipeConn is one end of an in-memory SignalConn pair.
type pipeConn struct {
	in, out chan []byte
	done    chan struct{}
	once    *sync.Once
}

func pipe() (a, b *pipeConn) {
	ab, ba := make(chan []byte, 16), make(chan []byte, 16)
	done := make(chan struct{})
	once := new(sync.Once)
	return &pipeConn{in: ba, out: ab, done: done, once: once}, &pipeConn{in: ab, out: ba, done: done, once: once}
}

func (c *pipeConn) ReadMessage() ([]byte, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.done:
		return nil, errors.New("closed")
	}
}

func (c *pipeConn) WriteMessage(data []byte) error {
	select {
	case c.out <- data:
		return nil
	case <-c.done:
		return errors.New("closed")
	}
}

func (c *pipeConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

// The wrapper keeps Serve's old contract: no credentials, no session.
func TestAgentStreamRunnerServe_NeedsAnAgentID(t *testing.T) {
	agent, _ := pipe()
	for _, r := range []*AgentStreamRunner{{}, NewAgentStreamRunner(nil, false), NewAgentStreamRunner(&AgentCredentials{}, false)} {
		if err := r.Serve(context.Background(), agent); !errors.Is(err, errNoAgentID) {
			t.Errorf("want errNoAgentID, got %v", err)
		}
	}
}

// A device key that cannot be loaded stops Serve rather than letting answers
// go out unsigned.
func TestAgentStreamRunnerServe_RefusesAnUnreadableKey(t *testing.T) {
	r := NewAgentStreamRunner(&AgentCredentials{AgentID: "a", DeviceKeyPath: filepath.Join(t.TempDir(), "missing.key")}, false)
	agent, _ := pipe()
	if err := r.Serve(context.Background(), agent); err == nil {
		t.Fatal("Serve with a missing device key: want an error")
	}
}

// The wrapper serves as the agent: its ID is the agent ID, and the answer to a
// viewer is signed with the device key from its credentials.
func TestAgentStreamRunnerServe_SignsWithTheDeviceKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.key")
	priv, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}
	r := NewAgentStreamRunner(&AgentCredentials{AgentID: "agent-1", DeviceKeyPath: path}, false)
	if r.ID() != "agent-1" {
		t.Fatalf("ID() = %q, want the agent ID", r.ID())
	}
	if got := r.Handle("x", func(context.Context, string, map[string]interface{}, func([]byte) error) {}); got != r {
		t.Fatal("Handle must return the wrapper, so chained calls keep tunnel's type")
	}

	viewer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = viewer.Close() }()
	if err := viewer.CreateVideoTrack("viewer", "v"); err != nil {
		t.Fatal(err)
	}
	offer, err := viewer.CreateOffer()
	if err != nil {
		t.Fatal(err)
	}
	offerMsg := webrtc.SignalMessage{Type: webrtc.SignalOffer, SessionID: "agent-1", SDP: offer}
	msg, err := offerMsg.Encode()
	if err != nil {
		t.Fatal(err)
	}

	agent, peer := pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Serve(ctx, agent) }()
	if err := peer.WriteMessage(msg); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case data := <-peer.in:
			if !bytes.Contains(data, []byte(`"answer"`)) {
				continue
			}
			m, err := webrtc.DecodeSignalMessage(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.VerifyFrom(priv.Public().(ed25519.PublicKey), time.Now()); err != nil {
				t.Fatalf("answer does not verify against the device key: %v", err)
			}
			return
		case <-deadline:
			t.Fatal("no answer from the agent")
		}
	}
}
