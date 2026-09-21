// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestTunnelClient_HandleReverseStream(t *testing.T) {
	// 1. Setup a local mock service
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on local mock port: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		if string(buf[:n]) == "PING\n" {
			_, _ = conn.Write([]byte("PONG\n"))
		}
	}()

	// 2. Setup Yamux over an in-memory pipe
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()

	// Server side yamux
	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer serverSession.Close()

	// Client side yamux
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer clientSession.Close()

	// 3. Open a stream from the server (simulating OpenReverseStream)
	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer stream.Close()

	// Accept the stream on the client side
	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("client accept stream failed: %v", err)
	}

	tc := &TunnelClient{}

	// Handle the reverse stream concurrently as done in Connect
	go tc.handleReverseStream(clientStream)

	// 4. Send the target port
	portLine := fmt.Sprintf("%d\n", port)
	_, err = stream.Write([]byte(portLine))
	if err != nil {
		t.Fatalf("failed to write port to stream: %v", err)
	}

	time.Sleep(50 * time.Millisecond) // Give handleReverseStream a moment to dial the local port

	// 5. Test bidirectional communication
	_, err = stream.Write([]byte("PING\n"))
	if err != nil {
		t.Fatalf("failed to write PING: %v", err)
	}

	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if string(buf[:n]) != "PONG\n" {
		t.Fatalf("expected 'PONG\\n', got %q", string(buf[:n]))
	}
}

func TestTunnelClient_HandleReverseStream_InvalidPort(t *testing.T) {
	// Setup Yamux over an in-memory pipe
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()

	// Server side yamux
	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer serverSession.Close()

	// Client side yamux
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer clientSession.Close()

	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer stream.Close()

	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("client accept stream failed: %v", err)
	}

	tc := &TunnelClient{}

	done := make(chan struct{})
	go func() {
		tc.handleReverseStream(clientStream)
		close(done)
	}()

	// Send an invalid port string
	_, err = stream.Write([]byte("invalid\n"))
	if err != nil {
		t.Fatalf("failed to write port to stream: %v", err)
	}

	// handleReverseStream should exit and close the stream
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("handleReverseStream did not return on invalid port")
	}
}

func TestTunnelClient_HandleReverseStream_DialFailure(t *testing.T) {
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()

	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer serverSession.Close()

	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer clientSession.Close()

	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer stream.Close()

	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("client accept stream failed: %v", err)
	}

	tc := &TunnelClient{}

	done := make(chan struct{})
	go func() {
		tc.handleReverseStream(clientStream)
		close(done)
	}()

	// Find a free port and ensure nothing is listening on it to simulate connection failure
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on local mock port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	portLine := fmt.Sprintf("%d\n", port)
	_, err = stream.Write([]byte(portLine))
	if err != nil {
		t.Fatalf("failed to write port to stream: %v", err)
	}

	// handleReverseStream should fail to connect and exit
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("handleReverseStream did not return on dial failure")
	}
}
