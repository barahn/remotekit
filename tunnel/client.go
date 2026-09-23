// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/barahn/remotekit/heartbeat"
	"github.com/barahn/remotekit/osinfo"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type AgentCredentials struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
	ServerAddr string `json:"server_addr"`
}

type TunnelClient struct {
	credsPath          string
	insecureSkipVerify bool

	// OnStatusChange, if set, is invoked whenever the tunnel's live connection
	// state changes: true right after the WebSocket/Yamux session is established,
	// false whenever Connect returns (error or clean shutdown). Callers use this
	// to drive UI indicators (e.g. the desktop tray icon) with the real connection
	// state instead of an assumed/static value.
	OnStatusChange func(connected bool)
}

func NewTunnelClient(credsPath string, insecureSkipVerify bool) *TunnelClient {
	if credsPath == "" {
		credsPath = "/etc/barahn/agent.pem" // #nosec G101 -- default path, not secret
	}
	if !insecureSkipVerify && os.Getenv("BARAHN_INSECURE_SKIP_VERIFY") == "true" {
		insecureSkipVerify = true
	}
	return &TunnelClient{
		credsPath:          credsPath,
		insecureSkipVerify: insecureSkipVerify,
	}
}

// Enroll performs single-use pairing exchange with the server and writes credentials to credsPath (/etc/barahn/agent.pem).
func Enroll(serverAddr, pairingCode, hostname, osName, arch, pubKey, savePath string, insecureSkipVerify bool) (*AgentCredentials, error) {
	if savePath == "" {
		savePath = "/etc/barahn/agent.pem"
	}

	if !insecureSkipVerify && os.Getenv("BARAHN_INSECURE_SKIP_VERIFY") == "true" {
		insecureSkipVerify = true
	}

	if insecureSkipVerify {
		fmt.Fprintln(os.Stderr, "[WARNING] TLS certificate verification is DISABLED (--insecure-skip-verify). Connection is insecure!")
	}

	parsedURL, err := url.ParseRequestURI(serverAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid server address: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid server address scheme: must be http or https")
	}
	if parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid server address host: host cannot be empty")
	}

	pairURL := parsedURL.JoinPath("tunnel", "pair").String()

	payload := map[string]string{
		"pairing_code": pairingCode,
		"hostname":     hostname,
		"os":           osName,
		"arch":         arch,
		"public_key":   pubKey,
	}

	bodyBytes, _ := json.Marshal(payload)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	if insecureSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- CLI opt-in flag for dev/test
		}
	}

	// #nosec G107 -- serverAddr is provided by the agent administrator during enrollment, not an untrusted user
	// nosemgrep: go.lang.security.audit.ssrf.ssrf
	resp, err := httpClient.Post(pairURL, "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to send pairing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pairing failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var creds AgentCredentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return nil, fmt.Errorf("failed to decode credentials response: %w", err)
	}

	creds.ServerAddr = serverAddr

	if err := saveCredentials(savePath, &creds); err != nil {
		return nil, fmt.Errorf("failed to save agent credentials to %s: %w", savePath, err)
	}

	return &creds, nil
}

func saveCredentials(path string, creds *AgentCredentials) error {
	cleanPath := filepath.Clean(path)
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cleanPath, data, 0600)
}

func LoadCredentials(path string) (*AgentCredentials, error) {
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("invalid credential path: path traversal detected")
	}

	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath) // #nosec G304 -- cleanPath is sanitized via filepath.Clean and checked for traversal
	if err != nil {
		return nil, err
	}

	var creds AgentCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	return &creds, nil
}

// Connect connects outbound over WebSocket/TLS 1.3 to the control plane, initializes Yamux, and accepts reverse streams.
func (tc *TunnelClient) Connect(ctx context.Context, creds *AgentCredentials) error {
	wsURL := creds.ServerAddr
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss" + wsURL[5:]
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws" + wsURL[4:]
	}

	wsURL = strings.TrimSuffix(wsURL, "/")
	wsURL += "/tunnel/connect"

	header := http.Header{}
	header.Set("X-Barahn-Agent-ID", creds.AgentID)
	header.Set("X-Barahn-Agent-Token", creds.AgentToken)

	sysInfo := osinfo.Detect()
	header.Set("X-Barahn-Agent-OS", sysInfo.Formatted)
	header.Set("X-Barahn-Agent-Arch", runtime.GOARCH)
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		header.Set("X-Barahn-Agent-Hostname", hostname)
	}

	dialer := websocket.DefaultDialer
	if tc.insecureSkipVerify {
		fmt.Fprintln(os.Stderr, "[WARNING] TLS certificate verification is DISABLED (--insecure-skip-verify). Connection is insecure!")
		dialer = &websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 45 * time.Second,
			TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- CLI opt-in flag for dev/test
		}
	}

	ws, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("failed to dial websocket tunnel: %w", err)
	}
	defer func() { _ = ws.Close() }()

	conn := &wsConnAdapter{Conn: ws}
	session, err := yamux.Client(conn, DefaultYamuxConfig())
	if err != nil {
		return fmt.Errorf("failed to establish yamux client session: %w", err)
	}
	defer func() { _ = session.Close() }()

	if tc.OnStatusChange != nil {
		tc.OnStatusChange(true)
		defer tc.OnStatusChange(false)
	}

	// Start heartbeat ticker over control stream
	chirpTicker := heartbeat.NewChirpTicker(10*time.Second, func(ctx context.Context) error {
		stream, err := session.OpenStream()
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()

		msg := heartbeat.ChirpMessage{
			AgentID:   creds.AgentID,
			Timestamp: time.Now().UTC(),
		}
		return json.NewEncoder(stream).Encode(msg)
	})
	chirpTicker.Start(ctx)
	defer chirpTicker.Stop()

	// Loop accepting reverse tunnel stream requests from server
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			stream, err := session.AcceptStream()
			if err != nil {
				if session.IsClosed() {
					return errors.New("yamux session closed")
				}
				continue
			}

			go tc.handleReverseStream(stream)
		}
	}
}

func (tc *TunnelClient) handleReverseStream(stream *yamux.Stream) {
	defer func() { _ = stream.Close() }()

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	portStr := strings.TrimSpace(line)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return
	}

	// Dial local service (e.g. 127.0.0.1:22 for SSH)
	targetAddr := fmt.Sprintf("127.0.0.1:%d", port)
	localConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = localConn.Close() }()

	// Proxy data bidirectionally
	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(localConn, reader)
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(stream, localConn)
		done <- struct{}{}
	}()

	<-done
}
