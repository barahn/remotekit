// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

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

	// The fields below authenticate the message; see Sign and Verify in
	// signed.go. All are empty on an unsigned message.

	// PubKey is the signer's Ed25519 public key, standard base64.
	PubKey string `json:"pub_key,omitempty"`

	// Nonce makes each signed message unique, so a NonceCache can refuse a
	// replay.
	Nonce string `json:"nonce,omitempty"`

	// IssuedAt and ExpiresAt bound the message's validity, in Unix seconds.
	IssuedAt  int64 `json:"issued_at,omitempty"`
	ExpiresAt int64 `json:"expires_at,omitempty"`

	// Sig is the Ed25519 signature over CanonicalBytes, standard base64.
	Sig string `json:"sig,omitempty"`
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
