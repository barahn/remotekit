// Package webrtc provides WebRTC peer connection management, SDP/ICE signaling,
// and embedded STUN/TURN relay server capabilities for Barahn remote sessions.
package webrtc

import (
	"encoding/json"
	"errors"
)

// SignalType defines the type of WebRTC signaling message.
type SignalType string

const (
	SignalOffer     SignalType = "offer"
	SignalAnswer    SignalType = "answer"
	SignalCandidate SignalType = "candidate"
	SignalClose     SignalType = "close"
)

// SignalMessage is exchanged between Agent, Control Plane Server, and Technician Web Viewer
// to establish P2P or TURN-relayed WebRTC media streams.
type SignalMessage struct {
	// Type is the signaling message category (offer, answer, candidate, close).
	Type SignalType `json:"type"`

	// SessionID is the unique remote support session identifier.
	SessionID string `json:"session_id,omitempty"`

	// TargetID identifies the intended recipient (Agent ID or User Session ID).
	TargetID string `json:"target_id,omitempty"`

	// SDP holds the Session Description Protocol payload for offer/answer.
	SDP string `json:"sdp,omitempty"`

	// Candidate holds the JSON-serialized ICE candidate.
	Candidate string `json:"candidate,omitempty"`

	// Error contains failure details if signaling fails.
	Error string `json:"error,omitempty"`
}

// Encode converts a SignalMessage to JSON bytes.
func (m *SignalMessage) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// DecodeSignalMessage parses JSON bytes into a SignalMessage.
func DecodeSignalMessage(data []byte) (*SignalMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("webrtc: empty signal message data")
	}
	var msg SignalMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
