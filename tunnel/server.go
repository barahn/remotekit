package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/mendsec/barahn/internal/storage"
	"github.com/mendsec/barahn/pkg/heartbeat"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 64,
	WriteBufferSize: 1024 * 64,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type TunnelServer struct {
	store      storage.Store
	sessions   map[string]*yamux.Session
	sessionsMu sync.RWMutex
	chirpMon   *heartbeat.ServerMonitor
}

func NewTunnelServer(store storage.Store) *TunnelServer {
	ts := &TunnelServer{
		store:    store,
		sessions: make(map[string]*yamux.Session),
	}
	ts.chirpMon = heartbeat.NewServerMonitor(3, func(ctx context.Context, agentID string) error {
		return store.UpdateAgentStatus(ctx, agentID, storage.StatusOffline)
	})

	// Background ticker to automatically mark stale agents offline every 10 seconds
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for range ticker.C {
			cutoff := time.Now().UTC().Add(-30 * time.Second)
			_ = store.CleanStaleAgentStatuses(context.Background(), cutoff)
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
		_, r, err := w.Conn.NextReader()
		if err != nil {
			return 0, err
		}
		w.r = r
	}
}

func (w *wsConnAdapter) Write(b []byte) (int, error) {
	err := w.Conn.WriteMessage(websocket.BinaryMessage, b)
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
	codeHash := hex.EncodeToString(sha256.New().Sum([]byte(req.PairingCode)))
	pc, err := ts.store.GetPairingCodeByHash(ctx, codeHash)
	if err != nil || pc.Used || time.Now().After(pc.ExpiresAt) {
		http.Error(w, "Invalid, expired, or already used pairing code", http.StatusUnauthorized)
		return
	}

	if err := ts.store.MarkPairingCodeUsed(ctx, pc.ID); err != nil {
		http.Error(w, "Failed to redeem pairing code", http.StatusInternalServerError)
		return
	}

	agent := &storage.Agent{
		ID:            uuid.New().String(),
		Hostname:      req.Hostname,
		OS:            req.OS,
		Arch:          req.Arch,
		PublicKey:     req.PublicKey,
		PairingCodeID: pc.ID,
		Status:        storage.StatusOnline,
	}

	if err := ts.store.CreateAgent(ctx, agent); err != nil {
		http.Error(w, "Failed to register agent", http.StatusInternalServerError)
		return
	}

	// Generate persistent agent credential token
	credToken := hex.EncodeToString(sha256.New().Sum([]byte(agent.ID + ":" + req.PublicKey)))

	resp := map[string]interface{}{
		"agent_id":     agent.ID,
		"agent_token":  credToken,
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

	expectedToken := hex.EncodeToString(sha256.New().Sum([]byte(agent.ID + ":" + agent.PublicKey)))
	if agentToken != expectedToken {
		http.Error(w, "Invalid agent token", http.StatusUnauthorized)
		return
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

	_ = ts.store.UpdateAgentStatus(ctx, agentID, storage.StatusOnline)

	// Keep-alive stream loop
	go ts.handleAgentControlStream(ctx, agentID, session)
}

func (ts *TunnelServer) handleAgentControlStream(ctx context.Context, agentID string, session *yamux.Session) {
	defer func() {
		ts.sessionsMu.Lock()
		delete(ts.sessions, agentID)
		ts.sessionsMu.Unlock()
		_ = session.Close()
		_ = ts.store.UpdateAgentStatus(context.Background(), agentID, storage.StatusOffline)
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
				_ = ts.store.UpdateAgentChirp(context.Background(), agentID, now)
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
