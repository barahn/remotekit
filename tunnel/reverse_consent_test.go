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

func TestCheckReversePort_Consent(t *testing.T) {
	asked := 0
	answer := true
	tc := &TunnelClient{
		AllowedReversePorts: []int{22},
		ConsentReverseStream: func(port int) bool {
			asked++
			return answer
		},
	}

	if err := tc.checkReversePort(22); err != nil {
		t.Fatalf("allowed and consented: %v", err)
	}

	answer = false
	if err := tc.checkReversePort(22); !errors.Is(err, ErrReverseStreamNotConsented) {
		t.Fatalf("declined: want ErrReverseStreamNotConsented, got %v", err)
	}

	// A port the allowlist refuses never reaches the consent prompt, so a
	// server cannot use refused ports to pester the user.
	asked = 0
	if err := tc.checkReversePort(23); !errors.Is(err, ErrPortNotPermitted) {
		t.Fatalf("unlisted port: want ErrPortNotPermitted, got %v", err)
	}
	if asked != 0 {
		t.Fatalf("consent was asked about a port the allowlist refuses")
	}

	// Without a consent hook the allowlist alone decides, as before.
	if err := (&TunnelClient{AllowedReversePorts: []int{22}}).checkReversePort(22); err != nil {
		t.Fatalf("no consent hook: %v", err)
	}
}

// Wired to the session's consent as ConsentReverseStream's doc comment shows:
// refused until granted, and refused again once the session revokes.
func TestCheckReversePort_FollowsSessionConsent(t *testing.T) {
	runner := &AgentStreamRunner{}
	tc := &TunnelClient{
		AllowedReversePorts: []int{22},
		ConsentReverseStream: func(int) bool {
			return runner.Granted(PermissionReverseStream)
		},
	}

	if err := tc.checkReversePort(22); !errors.Is(err, ErrReverseStreamNotConsented) {
		t.Fatalf("before Grant: want ErrReverseStreamNotConsented, got %v", err)
	}
	runner.Grant(PermissionReverseStream)
	if err := tc.checkReversePort(22); err != nil {
		t.Fatalf("after Grant: %v", err)
	}
	runner.Revoke()
	if err := tc.checkReversePort(22); !errors.Is(err, ErrReverseStreamNotConsented) {
		t.Fatalf("after Revoke: want ErrReverseStreamNotConsented, got %v", err)
	}
}

// End to end: an allowed port with a live listener is not dialled when consent
// is declined.
func TestHandleReverseStream_NotConsented(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := yamux.Client(p2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()
	stream, err := serverSession.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	clientStream, err := clientSession.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}

	tc := &TunnelClient{
		AllowedReversePorts:  []int{port},
		ConsentReverseStream: func(int) bool { return false },
	}
	done := make(chan struct{})
	go func() {
		tc.handleReverseStream(clientStream)
		close(done)
	}()

	if _, err := fmt.Fprintf(stream, "%d\n", port); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleReverseStream did not return for a declined stream")
	}
	select {
	case <-accepted:
		t.Fatal("a declined reverse stream was dialled anyway")
	case <-time.After(100 * time.Millisecond):
	}
}
