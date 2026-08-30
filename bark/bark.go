package bark

import (
	"encoding/json"
	"time"
)

// MessageType represents the identifier for a typed Bark protocol message.
type MessageType string

const (
	TypeChirpMessage           MessageType = "chirp"
	TypeSessionMessage         MessageType = "session"
	TypeInputEvent             MessageType = "input"
	TypeFileChunk              MessageType = "file_chunk"
	TypeChatMessage            MessageType = "chat"
	TypeScriptExecutionRequest MessageType = "script_exec_req"
	TypeScriptExecutionChunk   MessageType = "script_exec_chunk"
	TypeScriptExecutionResult  MessageType = "script_exec_res"
	TypeFocusStateMessage      MessageType = "focus_state"
	TypePowerActionMessage     MessageType = "power"
	TypeConsentRequest         MessageType = "consent_req"
	TypeConsentResponse        MessageType = "consent_res"
	TypeChirpSessionState      MessageType = "chirp_state"
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

// ScriptExecutionRequest represents a background script execution command (Issue #60).
type ScriptExecutionRequest struct {
	ExecutionID    string `json:"execution_id"`
	Interpreter    string `json:"interpreter"` // "powershell", "cmd", "bash", "sh"
	ScriptBody     string `json:"script_body"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	WorkingDir     string `json:"working_dir,omitempty"`
}

// ScriptExecutionChunk represents streamed stdout/stderr output from a running script.
type ScriptExecutionChunk struct {
	ExecutionID string `json:"execution_id"`
	Stream      string `json:"stream"` // "stdout" or "stderr"
	Data        string `json:"data"`
	Index       int    `json:"index"`
}

// ScriptExecutionResult represents the final completion result of a script.
type ScriptExecutionResult struct {
	ExecutionID         string `json:"execution_id"`
	ExitCode            int    `json:"exit_code"`
	ExecutionDurationMS int64  `json:"execution_duration_ms"`
	Error               string `json:"error,omitempty"`
}

// FocusStateMessage represents session focus synchronization events (Issue #61).
type FocusStateMessage struct {
	SessionID string `json:"session_id"`
	Focused   bool   `json:"focused"`
}

// PowerActionMessage represents remote workstation power instructions.
type PowerActionMessage struct {
	Action string `json:"action"` // "lock", "reboot", "shutdown"
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
