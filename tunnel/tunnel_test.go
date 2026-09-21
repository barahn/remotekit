// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/barahn/remotekit/tunnel"
)

func TestTunnel_EnrollmentAndReverseStream(t *testing.T) {
	tmpDir := t.TempDir()

	// Seed pairing code
	store := tunnel.NewMemStore()
	rawPairingCode := "PAIR1234"
	codeHash := hex.EncodeToString(sha256.New().Sum([]byte(rawPairingCode)))
	store.SeedPairingCode(codeHash, tunnel.PairingCode{
		ID:        "pc-99",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})

	tunnelServer := tunnel.NewTunnelServer(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/pair", tunnelServer.HandlePairing)
	mux.HandleFunc("/tunnel/connect", tunnelServer.HandleConnect)

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Agent Enroll
	savePath := filepath.Join(tmpDir, "agent.pem")
	creds, err := tunnel.Enroll(server.URL, rawPairingCode, "test-host", "linux", "amd64", "pubkey-123", savePath, false)
	if err != nil {
		t.Fatalf("Enroll failed: %v", err)
	}

	if creds.AgentID == "" || creds.AgentToken == "" {
		t.Fatalf("Enroll return empty credentials: %+v", creds)
	}

	// 2. Start mock local SSH target listener on random TCP port
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen on local mock SSH port: %v", err)
	}
	defer sshListener.Close()

	sshPort := sshListener.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := sshListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				if string(buf[:n]) == "PING_SSH\n" {
					_, _ = c.Write([]byte("PONG_SSH\n"))
				}
			}(conn)
		}
	}()

	// 3. Connect Agent Tunnel in background
	client := tunnel.NewTunnelClient(savePath, false)
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()

	go func() {
		_ = client.Connect(connCtx, creds)
	}()

	time.Sleep(200 * time.Millisecond) // Allow websocket and yamux session setup

	// 4. Open Reverse Stream from Server to Agent target port
	revConn, err := tunnelServer.OpenReverseStream(creds.AgentID, sshPort)
	if err != nil {
		t.Fatalf("OpenReverseStream failed: %v", err)
	}
	defer revConn.Close()

	// 5. Test bidirectional stream communication over Yamux
	_, err = revConn.Write([]byte("PING_SSH\n"))
	if err != nil {
		t.Fatalf("Failed to write to reverse stream: %v", err)
	}

	respBuf := make([]byte, 64)
	n, err := revConn.Read(respBuf)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from reverse stream: %v", err)
	}

	if string(respBuf[:n]) != "PONG_SSH\n" {
		t.Fatalf("Reverse stream output mismatch. Expected 'PONG_SSH\\n', got: %q", string(respBuf[:n]))
	}
}

func TestTunnel_TLSVerification_UntrustedCertFails(t *testing.T) {
	tmpDir := t.TempDir()

	store := tunnel.NewMemStore()
	rawPairingCode := "TLSCODE1"
	codeHash := hex.EncodeToString(sha256.New().Sum([]byte(rawPairingCode)))
	store.SeedPairingCode(codeHash, tunnel.PairingCode{
		ID:        "pc-tls",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})

	tunnelServer := tunnel.NewTunnelServer(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/pair", tunnelServer.HandlePairing)
	mux.HandleFunc("/tunnel/connect", tunnelServer.HandleConnect)

	// Launch TLS server with self-signed certificate untrusted by default CA pool
	tlsServer := httptest.NewTLSServer(mux)
	defer tlsServer.Close()

	savePath := filepath.Join(tmpDir, "agent_tls.pem")

	// Case 1: Default TLS verification ON (insecureSkipVerify = false) -> MUST FAIL on untrusted cert
	_, err := tunnel.Enroll(tlsServer.URL, rawPairingCode, "tls-host", "linux", "amd64", "pubkey-tls", savePath, false)
	if err == nil {
		t.Fatalf("Expected Enroll to fail due to untrusted TLS certificate when insecureSkipVerify is false, but it succeeded")
	}

	// Case 2: Opt-in bypass (insecureSkipVerify = true) -> MUST SUCCEED
	creds, err := tunnel.Enroll(tlsServer.URL, rawPairingCode, "tls-host", "linux", "amd64", "pubkey-tls", savePath, true)
	if err != nil {
		t.Fatalf("Expected Enroll to succeed with insecureSkipVerify = true, but failed: %v", err)
	}
	if creds == nil || creds.AgentID == "" {
		t.Fatalf("Enroll returned invalid credentials with insecureSkipVerify = true")
	}
}

func TestLoadCredentials_PathTraversal(t *testing.T) {
	// 1. Setup valid credential file
	tmpDir := t.TempDir()
	validPath := filepath.Join(tmpDir, "valid_agent.pem")

	// Create some dummy valid content
	validCreds := `{"agent_id":"123","agent_token":"abc","server_addr":"http://test"}`
	if err := os.WriteFile(validPath, []byte(validCreds), 0600); err != nil {
		t.Fatalf("failed to write valid creds: %v", err)
	}

	// 2. Test Path Traversal
	traversalPath := tmpDir + "/../some_other_dir/agent.pem"
	_, err := tunnel.LoadCredentials(traversalPath)
	if err == nil {
		t.Fatalf("Expected LoadCredentials to fail for path containing '..', but it succeeded")
	}
	if err.Error() != "invalid credential path: path traversal detected" {
		t.Fatalf("Expected path traversal error, got: %v", err)
	}

	// 3. Test Valid Path
	creds, err := tunnel.LoadCredentials(validPath)
	if err != nil {
		t.Fatalf("Expected LoadCredentials to succeed for valid path, but failed: %v", err)
	}
	if creds.AgentID != "123" {
		t.Fatalf("Unexpected credential content: %+v", creds)
	}
}
