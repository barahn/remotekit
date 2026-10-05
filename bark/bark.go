// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package bark

import (
	"encoding/json"
	"time"
)

// MessageType represents the identifier for a typed Bark protocol message.
type MessageType string

const (
	TypeChirpMessage      MessageType = "chirp"
	TypeSessionMessage    MessageType = "session"
	TypeInputEvent        MessageType = "input"
	TypeFileChunk         MessageType = "file_chunk"
	TypeChatMessage       MessageType = "chat"
	TypeFocusStateMessage MessageType = "focus_state"
	TypeConsentRequest    MessageType = "consent_req"
	TypeConsentResponse   MessageType = "consent_res"
	TypeChirpSessionState MessageType = "chirp_state"

	// Data plane messages carried over a WebRTC data channel; see
	// tunnel.AgentStreamRunner.RequireDataChannel. Their payloads use the
	// same field names as the signalling-socket messages of the same type.
	TypeClipboard    MessageType = "clipboard"
	TypeFileStart    MessageType = "file_start"
	TypeFileComplete MessageType = "file_complete"
	TypeFileProgress MessageType = "file_progress"
	TypeFileSaved    MessageType = "file_saved"
)

// Envelope is the top-level wire format for Yamux Bark multiplexed streams.
type Envelope struct {
	Type      MessageType     `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// ChirpMessage represents Woodstock presence / heartbeat signals (agent -> server).
type ChirpMessage struct {
	AgentID   string    `json:"agent_id"`
	Timestamp time.Time `json:"timestamp"`
}

// SessionMessage represents control signals between server and agent.
type SessionMessage struct {
	SessionID string `json:"session_id"`
	Action    string `json:"action"` // "start", "stop", "pause", "resume"
	Reason    string `json:"reason,omitempty"`
}

// InputEvent represents mouse and keyboard injection events (server/browser -> agent).
type InputEvent struct {
	Type   string  `json:"type"` // "mouse_move", "mouse_down", "mouse_up", "scroll", "key_down", "key_up"
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button int     `json:"button,omitempty"`
	DeltaX float64 `json:"deltaX,omitempty"`
	DeltaY float64 `json:"deltaY,omitempty"`
	Key    string  `json:"key,omitempty"`
	Code   string  `json:"code,omitempty"`
	Ctrl   bool    `json:"ctrl,omitempty"`
	Alt    bool    `json:"alt,omitempty"`
	Shift  bool    `json:"shift,omitempty"`
	Meta   bool    `json:"meta,omitempty"`
}

// FileChunk represents bidirectional chunked file transfer data.
type FileChunk struct {
	TransferID string `json:"transfer_id"`
	Index      int    `json:"index"`
	Data       string `json:"data"` // Base64 encoded payload
	Done       bool   `json:"done,omitempty"`
}

// ChatMessage represents in-session text chat between technician and client.
type ChatMessage struct {
	SessionID string    `json:"session_id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// FocusStateMessage represents session focus synchronization events (Issue #61).
type FocusStateMessage struct {
	SessionID string `json:"session_id"`
	Focused   bool   `json:"focused"`
}

// ConsentRequestPayload represents an authorization request sent to the end-user (Issue #12).
type ConsentRequestPayload struct {
	SessionID            string    `json:"session_id"`
	Code                 string    `json:"code"`
	TechnicianName       string    `json:"technician_name"`
	RequestedPermissions []string  `json:"requested_permissions"` // e.g. ["screen_view", "remote_control"]
	Timestamp            time.Time `json:"timestamp"`
}

// ConsentResponsePayload represents the end-user authorization decision (Issue #12).
type ConsentResponsePayload struct {
	SessionID string    `json:"session_id"`
	Code      string    `json:"code"`
	Granted   bool      `json:"granted"`
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ChirpStatePayload represents state updates for on-demand Chirp sessions.
type ChirpStatePayload struct {
	SessionID string    `json:"session_id"`
	Code      string    `json:"code"`
	State     string    `json:"state"` // "pending", "connected", "consent_requested", "active", "rejected", "expired", "terminated"
	ExpiresAt time.Time `json:"expires_at"`
}

// EncodeEnvelope serializes a payload into a Bark envelope JSON bytes.
func EncodeEnvelope(msgType MessageType, sessionID string, payload interface{}) ([]byte, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	env := Envelope{
		Type:      msgType,
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Payload:   payloadBytes,
	}
	return json.Marshal(env)
}
