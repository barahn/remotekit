// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"errors"
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
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		if string(buf[:n]) == "PING\n" {
			_, _ = conn.Write([]byte("PONG\n"))
		}
	}()

	// 2. Setup Yamux over an in-memory pipe
	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close() }()
	defer func() { _ = p2.Close() }()

	// Server side yamux
	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	// Client side yamux
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	// 3. Open a stream from the server (simulating OpenReverseStream)
	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Accept the stream on the client side
	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("client accept stream failed: %v", err)
	}

	tc := &TunnelClient{AllowedReversePorts: []int{port}}

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
	defer func() { _ = p1.Close() }()
	defer func() { _ = p2.Close() }()

	// Server side yamux
	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	// Client side yamux
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

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
	defer func() { _ = p1.Close() }()
	defer func() { _ = p2.Close() }()

	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("server open stream failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("client accept stream failed: %v", err)
	}

	// Find a free port and ensure nothing is listening on it to simulate connection failure
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on local mock port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	// Allowed, so what is exercised is the dial failing, not the allowlist.
	tc := &TunnelClient{AllowedReversePorts: []int{port}}

	done := make(chan struct{})
	go func() {
		tc.handleReverseStream(clientStream)
		close(done)
	}()

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

// A port the server asks for but the agent has not listed must not be dialled,
// even when something is listening on it.
func TestTunnelClient_HandleReverseStream_PortNotPermitted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
		accepted <- struct{}{}
	}()

	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close() }()
	defer func() { _ = p2.Close() }()
	serverSession, err := yamux.Server(p1, nil)
	if err != nil {
		t.Fatalf("yamux.Server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()
	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	// 22 is allowed; the requested port is not.
	tc := &TunnelClient{AllowedReversePorts: []int{22}}
	done := make(chan struct{})
	go func() {
		tc.handleReverseStream(clientStream)
		close(done)
	}()

	if _, err := fmt.Fprintf(stream, "%d\n", port); err != nil {
		t.Fatalf("write port: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleReverseStream did not return for a refused port")
	}
	select {
	case <-accepted:
		t.Fatal("refused port was dialled anyway")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTunnelClient_CheckReversePort(t *testing.T) {
	tc := &TunnelClient{AllowedReversePorts: []int{22, 5900}}
	for _, p := range []int{22, 5900} {
		if err := tc.checkReversePort(p); err != nil {
			t.Errorf("port %d should be permitted: %v", p, err)
		}
	}
	if err := tc.checkReversePort(23); !errors.Is(err, ErrPortNotPermitted) {
		t.Errorf("port 23: want ErrPortNotPermitted, got %v", err)
	}
	// The zero value refuses everything rather than allowing everything.
	if err := (&TunnelClient{}).checkReversePort(22); !errors.Is(err, ErrPortNotPermitted) {
		t.Errorf("empty allowlist: want ErrPortNotPermitted, got %v", err)
	}
}

func TestSignalHeaders_RequiresToken(t *testing.T) {
	// The agent ID is not a secret and must never stand in for the token.
	if _, err := signalHeaders(&AgentCredentials{AgentID: "agent-1"}); !errors.Is(err, errNoAgentToken) {
		t.Fatalf("empty token: want errNoAgentToken, got %v", err)
	}
	if _, err := signalHeaders(nil); !errors.Is(err, errNoAgentToken) {
		t.Fatalf("nil creds: want errNoAgentToken, got %v", err)
	}
	h, err := signalHeaders(&AgentCredentials{AgentID: "agent-1", AgentToken: "tok"})
	if err != nil {
		t.Fatalf("with token: %v", err)
	}
	if got := h.Get("X-Barahn-Agent-Token"); got != "tok" {
		t.Fatalf("token header = %q, want %q", got, "tok")
	}
}
