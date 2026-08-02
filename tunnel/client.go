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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/mendsec/barahn/pkg/heartbeat"
)

type AgentCredentials struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
	ServerAddr string `json:"server_addr"`
}

type TunnelClient struct {
	credsPath string
}

func NewTunnelClient(credsPath string) *TunnelClient {
	if credsPath == "" {
		credsPath = "/etc/barahn/agent.pem"
	}
	return &TunnelClient{credsPath: credsPath}
}

// Enroll performs single-use pairing exchange with the server and writes credentials to credsPath (/etc/barahn/agent.pem).
func Enroll(serverAddr, pairingCode, hostname, osName, arch, pubKey, savePath string) (*AgentCredentials, error) {
	if savePath == "" {
		savePath = "/etc/barahn/agent.pem"
	}

	payload := map[string]string{
		"pairing_code": pairingCode,
		"hostname":     hostname,
		"os":           osName,
		"arch":         arch,
		"public_key":   pubKey,
	}

	bodyBytes, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/tunnel/pair", strings.TrimRight(serverAddr, "/"))

	// Create custom client allowing self-signed TLS in dev/test if needed
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	resp, err := httpClient.Post(url, "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to send pairing request: %w", err)
	}
	defer resp.Body.Close()

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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

func LoadCredentials(path string) (*AgentCredentials, error) {
	data, err := os.ReadFile(path)
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
	wsURL := strings.Replace(creds.ServerAddr, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = fmt.Sprintf("%s/tunnel/connect", strings.TrimRight(wsURL, "/"))

	header := http.Header{}
	header.Set("X-Barahn-Agent-ID", creds.AgentID)
	header.Set("X-Barahn-Agent-Token", creds.AgentToken)

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	ws, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("failed to dial websocket tunnel: %w", err)
	}
	defer ws.Close()

	conn := &wsConnAdapter{Conn: ws}
	session, err := yamux.Client(conn, DefaultYamuxConfig())
	if err != nil {
		return fmt.Errorf("failed to establish yamux client session: %w", err)
	}
	defer session.Close()

	// Start heartbeat ticker over control stream
	chirpTicker := heartbeat.NewChirpTicker(10*time.Second, func(ctx context.Context) error {
		stream, err := session.OpenStream()
		if err != nil {
			return err
		}
		defer stream.Close()

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
	defer stream.Close()

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
	defer localConn.Close()

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
