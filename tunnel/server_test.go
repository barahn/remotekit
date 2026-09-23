// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestTunnelServer_Multiplexing(t *testing.T) {
	ts := NewTunnelServer(NewMemStore())

	clientConn, serverConn := net.Pipe()

	serverSession, err := yamux.Server(serverConn, DefaultYamuxConfig())
	if err != nil {
		t.Fatalf("Failed to create yamux server: %v", err)
	}

	clientSession, err := yamux.Client(clientConn, DefaultYamuxConfig())
	if err != nil {
		t.Fatalf("Failed to create yamux client: %v", err)
	}

	agentID := "agent-multiplex-1"

	// Inject session manually
	ts.sessionsMu.Lock()
	ts.sessions[agentID] = serverSession
	ts.sessionsMu.Unlock()

	if !ts.IsSessionConnected(agentID) {
		t.Errorf("Expected session to be connected")
	}

	// Test reverse stream
	targetPort := 2222

	errCh := make(chan error, 1)
	go func() {
		stream, err := clientSession.AcceptStream()
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = stream.Close() }()

		buf := make([]byte, 64)
		n, err := stream.Read(buf)
		if err != nil {
			errCh <- err
			return
		}

		if string(buf[:n]) != "2222\n" {
			t.Errorf("Expected 2222\\n, got %s", string(buf[:n]))
		}

		_, err = stream.Write([]byte("ACK"))
		if err != nil {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	stream, err := ts.OpenReverseStream(agentID, targetPort)
	if err != nil {
		t.Fatalf("OpenReverseStream failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	buf := make([]byte, 3)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read from reverse stream: %v", err)
	}

	if string(buf[:n]) != "ACK" {
		t.Errorf("Expected ACK, got %s", string(buf[:n]))
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Client side error: %v", err)
		}
	case <-time.After(time.Second):
		t.Errorf("Timeout waiting for client side")
	}

	// Test disconnected state
	_ = clientSession.Close()
	_ = serverSession.Close()
	time.Sleep(10 * time.Millisecond) // Let closes propagate

	if ts.IsSessionConnected(agentID) {
		t.Errorf("Expected session to be disconnected")
	}
}

func TestTunnelServer_OpenReverseStream_Offline(t *testing.T) {
	ts := NewTunnelServer(NewMemStore())
	agentID := "offline-agent"

	_, err := ts.OpenReverseStream(agentID, 22)
	if err == nil {
		t.Errorf("Expected error for offline agent, got nil")
	}
	if err.Error() != "agent session offline or not connected" {
		t.Errorf("Expected offline error message, got: %v", err)
	}
}
