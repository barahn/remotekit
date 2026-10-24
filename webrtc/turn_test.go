// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G401,G505 -- testing RFC/TURN REST API conformance
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func TestGenerateEphemeralTURNCredentials_Format(t *testing.T) {
	sharedSecret := "super-secret-key-123"
	username := "test-agent-42"
	ttl := 10 * time.Minute

	before := time.Now().Unix()
	user, pass, expiresAt := GenerateEphemeralTURNCredentials(sharedSecret, username, ttl)
	after := time.Now().Unix()

	// Verify expiry
	if expiresAt < before+int64(ttl.Seconds()) || expiresAt > after+int64(ttl.Seconds()) {
		t.Errorf("expiresAt = %d, expected around %d", expiresAt, before+int64(ttl.Seconds()))
	}

	// Verify username format: <timestamp>:<username>
	parts := strings.SplitN(user, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("user %q does not match format <timestamp>:<username>", user)
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("failed to parse timestamp in user %q: %v", user, err)
	}
	if ts != expiresAt {
		t.Errorf("timestamp in user %d != expiresAt %d", ts, expiresAt)
	}
	if parts[1] != username {
		t.Errorf("username in user %q != expected %q", parts[1], username)
	}

	// Verify HMAC-SHA1 calculation
	mac := hmac.New(sha1.New, []byte(sharedSecret)) // #nosec G401,G505
	_, _ = mac.Write([]byte(user))
	expectedPass := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if pass != expectedPass {
		t.Errorf("password = %q, want %q", pass, expectedPass)
	}

	// Test without username (empty string)
	emptyUser, emptyPass, emptyExp := GenerateEphemeralTURNCredentials(sharedSecret, "", ttl)
	if emptyExp != expiresAt && emptyExp != expiresAt+1 {
		t.Errorf("emptyExp = %d, expected around %d", emptyExp, expiresAt)
	}
	if emptyUser != strconv.FormatInt(emptyExp, 10) {
		t.Errorf("empty username user = %q, want %d", emptyUser, emptyExp)
	}
	macEmpty := hmac.New(sha1.New, []byte(sharedSecret)) // #nosec G401,G505
	_, _ = macEmpty.Write([]byte(emptyUser))
	expectedEmptyPass := base64.StdEncoding.EncodeToString(macEmpty.Sum(nil))
	if emptyPass != expectedEmptyPass {
		t.Errorf("empty username password = %q, want %q", emptyPass, expectedEmptyPass)
	}
}

func startTestTURNServer(t *testing.T, sharedSecret string) (*turn.Server, net.Addr) {
	t.Helper()

	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen packet for TURN server: %v", err)
	}

	server, err := turn.NewServer(turn.ServerConfig{
		AuthHandler: turn.LongTermTURNRESTAuthHandler(sharedSecret, nil),
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP("127.0.0.1"),
					Address:      "127.0.0.1",
				},
			},
		},
		Realm:         "remotekit.test",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		_ = serverListener.Close()
		t.Fatalf("failed to create turn server: %v", err)
	}

	t.Cleanup(func() {
		_ = server.Close()
	})

	return server, serverListener.LocalAddr()
}

func TestGenerateEphemeralTURNCredentials_PionTURNServer(t *testing.T) {
	const sharedSecret = "remotekit-turn-shared-secret"
	_, serverAddr := startTestTURNServer(t, sharedSecret)

	// 1. Generate valid credentials with 5 minute TTL
	user, pass, _ := GenerateEphemeralTURNCredentials(sharedSecret, "client-1", 5*time.Minute)

	clientConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for turn client: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: serverAddr.String(),
		TURNServerAddr: serverAddr.String(),
		Conn:           clientConn,
		Username:       user,
		Password:       pass,
		Realm:          "remotekit.test",
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("failed to create turn client: %v", err)
	}
	defer client.Close()

	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen failed: %v", err)
	}

	// Allocation must succeed with valid ephemeral credentials
	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("client.Allocate failed with valid credentials: %v", err)
	}
	defer func() { _ = relayConn.Close() }()

	if relayConn.LocalAddr() == nil {
		t.Errorf("relayConn LocalAddr is nil")
	}
}

func TestGenerateEphemeralTURNCredentials_ExpiredRefused(t *testing.T) {
	const sharedSecret = "remotekit-turn-shared-secret"
	_, serverAddr := startTestTURNServer(t, sharedSecret)

	// 1. Generate expired credentials with negative TTL
	user, pass, _ := GenerateEphemeralTURNCredentials(sharedSecret, "client-expired", -5*time.Minute)

	clientConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for turn client: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: serverAddr.String(),
		TURNServerAddr: serverAddr.String(),
		Conn:           clientConn,
		Username:       user,
		Password:       pass,
		Realm:          "remotekit.test",
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("failed to create turn client: %v", err)
	}
	defer client.Close()

	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen failed: %v", err)
	}

	// Allocation must fail because credentials are expired
	relayConn, err := client.Allocate()
	if err == nil {
		_ = relayConn.Close()
		t.Fatal("client.Allocate succeeded unexpectedly with expired credentials")
	}
	t.Logf("client.Allocate correctly rejected expired credentials: %v", err)
}

func TestGenerateEphemeralTURNCredentials_InvalidSecretRefused(t *testing.T) {
	const serverSecret = "server-secret"
	const wrongSecret = "wrong-secret"
	_, serverAddr := startTestTURNServer(t, serverSecret)

	// Generate credentials with a secret that does not match serverSecret
	user, pass, _ := GenerateEphemeralTURNCredentials(wrongSecret, "client-wrong", 5*time.Minute)

	clientConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for turn client: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: serverAddr.String(),
		TURNServerAddr: serverAddr.String(),
		Conn:           clientConn,
		Username:       user,
		Password:       pass,
		Realm:          "remotekit.test",
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("failed to create turn client: %v", err)
	}
	defer client.Close()

	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen failed: %v", err)
	}

	// Allocation must fail due to HMAC mismatch
	relayConn, err := client.Allocate()
	if err == nil {
		_ = relayConn.Close()
		t.Fatal("client.Allocate succeeded unexpectedly with invalid secret")
	}
	t.Logf("client.Allocate correctly rejected invalid secret: %v", err)
}

func TestGenerateEphemeralTURNCredentials_PeerConfigUsage(t *testing.T) {
	// Demonstrates and verifies consumer usage pattern for Issue #64:
	// Passing generated ephemeral credentials into PeerConfig.
	sharedSecret := "turn-secret-token"
	turnHost := "turn.example.test:3478"
	user, pass, expiresAt := GenerateEphemeralTURNCredentials(sharedSecret, "session-user-1", 15*time.Minute)

	cfg := PeerConfig{
		ICEServers: []string{
			fmt.Sprintf("turn:%s", turnHost),
		},
		TURNUsername: user,
		TURNPassword: pass,
	}

	if len(cfg.ICEServers) != 1 || cfg.ICEServers[0] != "turn:turn.example.test:3478" {
		t.Errorf("unexpected ICEServers: %v", cfg.ICEServers)
	}
	if cfg.TURNUsername != user || !strings.Contains(cfg.TURNUsername, "session-user-1") {
		t.Errorf("unexpected TURNUsername: %s", cfg.TURNUsername)
	}
	if cfg.TURNPassword != pass || cfg.TURNPassword == "" {
		t.Errorf("unexpected TURNPassword: %s", cfg.TURNPassword)
	}
	if expiresAt <= time.Now().Unix() {
		t.Errorf("expiresAt %d should be in the future", expiresAt)
	}
}
