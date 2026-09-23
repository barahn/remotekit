// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/barahn/remotekit/heartbeat"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 64,
	WriteBufferSize: 1024 * 64,
}

type TunnelServer struct {
	store      AgentStore
	sessions   map[string]*yamux.Session
	sessionsMu sync.RWMutex
	chirpMon   *heartbeat.ServerMonitor
}

func NewTunnelServer(store AgentStore) *TunnelServer {
	ts := &TunnelServer{
		store:    store,
		sessions: make(map[string]*yamux.Session),
	}
	ts.chirpMon = heartbeat.NewServerMonitor(3, func(ctx context.Context, agentID string) error {
		return store.SetAgentOffline(ctx, agentID)
	})

	// Background ticker to automatically mark stale agents offline every 10 seconds
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for range ticker.C {
			cutoff := time.Now().UTC().Add(-30 * time.Second)
			_ = store.CleanStaleAgents(context.Background(), cutoff)
		}
	}()

	return ts
}

// wsConnAdapter adapts a gorilla websocket to a net.Conn interface for yamux.
type wsConnAdapter struct {
	*websocket.Conn
	r io.Reader
}

func (w *wsConnAdapter) Read(b []byte) (int, error) {
	for {
		if w.r != nil {
			n, err := w.r.Read(b)
			if err == io.EOF {
				w.r = nil
			} else {
				return n, err
			}
		}
		_, r, err := w.NextReader()
		if err != nil {
			return 0, err
		}
		w.r = r
	}
}

func (w *wsConnAdapter) Write(b []byte) (int, error) {
	err := w.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *wsConnAdapter) SetDeadline(t time.Time) error {
	_ = w.SetReadDeadline(t)
	return w.SetWriteDeadline(t)
}

// HandlePairing handles one-time pairing requests from agents.
func (ts *TunnelServer) HandlePairing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		PairingCode string `json:"pairing_code"`
		Hostname    string `json:"hostname"`
		OS          string `json:"os"`
		Arch        string `json:"arch"`
		PublicKey   string `json:"public_key"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	codeHash := HashCredential(req.PairingCode)
	pc, err := ts.store.GetPairingCodeByHash(ctx, codeHash)
	if err != nil || pc.Used || time.Now().After(pc.ExpiresAt) {
		http.Error(w, "Invalid, expired, or already used pairing code", http.StatusUnauthorized)
		return
	}

	if err := ts.store.MarkPairingCodeUsed(ctx, pc.ID); err != nil {
		http.Error(w, "Failed to redeem pairing code", http.StatusInternalServerError)
		return
	}

	agent := AgentRegistration{
		ID:            uuid.New().String(),
		Hostname:      req.Hostname,
		OS:            req.OS,
		Arch:          req.Arch,
		PublicKey:     req.PublicKey,
		PairingCodeID: pc.ID,
	}

	// The reconnection credential is random, and only its hash is stored.
	//
	// It used to be derived from the agent id and its public key, both of
	// which the server itself hands out, so anyone holding them could mint the
	// token and connect as that agent. The derivation also called
	// sha256.New().Sum(data), which appends the digest to data rather than
	// hashing it, so the "hash" was the plaintext with a constant suffix.
	credToken, err := newAgentToken()
	if err != nil {
		http.Error(w, "Failed to issue agent credential", http.StatusInternalServerError)
		return
	}
	agent.TokenHash = HashCredential(credToken)

	if err := ts.store.CreateAgent(ctx, agent); err != nil {
		http.Error(w, "Failed to register agent", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"agent_id":      agent.ID,
		"agent_token":   credToken,
		"server_status": "enrolled",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleConnect upgrades outbound agent WebSocket connection to a multiplexed Yamux session.
func (ts *TunnelServer) HandleConnect(w http.ResponseWriter, r *http.Request) {
	agentID := r.Header.Get("X-Barahn-Agent-ID")
	agentToken := r.Header.Get("X-Barahn-Agent-Token")

	if agentID == "" || agentToken == "" {
		http.Error(w, "Missing agent authentication headers", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()
	agent, err := ts.store.GetAgentByID(ctx, agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusUnauthorized)
		return
	}

	// An agent with no stored hash predates the random credential and must
	// re-enrol; it must not fall through to a comparison that could match.
	if agent.TokenHash == "" ||
		subtle.ConstantTimeCompare([]byte(HashCredential(agentToken)), []byte(agent.TokenHash)) != 1 {
		http.Error(w, "Invalid agent token", http.StatusUnauthorized)
		return
	}

	// Update OS and Hostname if provided and changed
	agentOS := r.Header.Get("X-Barahn-Agent-OS")
	agentHostname := r.Header.Get("X-Barahn-Agent-Hostname")
	agentArch := r.Header.Get("X-Barahn-Agent-Arch")
	if agentOS != "" || agentHostname != "" || agentArch != "" {
		hostname := agent.Hostname
		if agentHostname != "" {
			hostname = agentHostname
		}
		osStr := agent.OS
		if agentOS != "" {
			osStr = agentOS
		}
		arch := agent.Arch
		if agentArch != "" {
			arch = agentArch
		}
		_ = ts.store.UpdateAgentInfo(ctx, agentID, hostname, osStr, arch)
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	conn := &wsConnAdapter{Conn: ws}
	session, err := yamux.Server(conn, DefaultYamuxConfig())
	if err != nil {
		_ = ws.Close()
		return
	}

	ts.sessionsMu.Lock()
	ts.sessions[agentID] = session
	ts.sessionsMu.Unlock()

	_ = ts.store.SetAgentOnline(ctx, agentID)

	// Keep-alive stream loop
	go ts.handleAgentControlStream(ctx, agentID, session)
}

func (ts *TunnelServer) handleAgentControlStream(ctx context.Context, agentID string, session *yamux.Session) {
	defer func() {
		ts.sessionsMu.Lock()
		delete(ts.sessions, agentID)
		ts.sessionsMu.Unlock()
		_ = session.Close()
		_ = ts.store.SetAgentOffline(context.Background(), agentID)
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}

		go func(s *yamux.Stream) { // #nosec G118 -- stream handling goroutine
			defer func() { _ = s.Close() }()
			var msg heartbeat.ChirpMessage
			if err := json.NewDecoder(s).Decode(&msg); err == nil {
				now := time.Now().UTC()
				_ = ts.store.RecordAgentChirp(context.Background(), agentID, now)
			}
		}(stream)
	}
}

func (ts *TunnelServer) IsSessionConnected(agentID string) bool {
	ts.sessionsMu.RLock()
	defer ts.sessionsMu.RUnlock()
	sess, ok := ts.sessions[agentID]
	return ok && sess != nil && !sess.IsClosed()
}

// OpenReverseStream opens a reverse stream over Yamux session to an agent's target port (e.g. 22).
func (ts *TunnelServer) OpenReverseStream(agentID string, targetPort int) (net.Conn, error) {
	ts.sessionsMu.RLock()
	session, ok := ts.sessions[agentID]
	ts.sessionsMu.RUnlock()

	if !ok || session.IsClosed() {
		return nil, errors.New("agent session offline or not connected")
	}

	stream, err := session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("failed to open reverse yamux stream: %w", err)
	}

	// Write target port header
	portMsg := fmt.Sprintf("%d\n", targetPort)
	if _, err := stream.Write([]byte(portMsg)); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write target port to reverse stream: %w", err)
	}

	return stream, nil
}
