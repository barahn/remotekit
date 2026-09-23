// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"time"
)

// AgentStore is the persistence surface the tunnel server needs in order to
// enrol agents and track their liveness. It is deliberately narrow and defined
// in terms of this package's own types so that the tunnel never depends on any
// particular storage implementation.
type AgentStore interface {
	// GetPairingCodeByHash looks up a pairing code by the hex-encoded hash of
	// its plaintext value.
	GetPairingCodeByHash(ctx context.Context, codeHash string) (PairingCode, error)
	// MarkPairingCodeUsed redeems a pairing code so it cannot be reused.
	MarkPairingCodeUsed(ctx context.Context, id string) error

	// CreateAgent registers a newly enrolled agent.
	CreateAgent(ctx context.Context, agent AgentRegistration) error
	// GetAgentByID returns the identity of an already enrolled agent.
	GetAgentByID(ctx context.Context, agentID string) (AgentIdentity, error)
	// UpdateAgentInfo refreshes the host details an agent reports on connect.
	UpdateAgentInfo(ctx context.Context, agentID, hostname, osName, arch string) error

	// SetAgentOnline marks an agent as currently connected.
	SetAgentOnline(ctx context.Context, agentID string) error
	// SetAgentOffline marks an agent as disconnected.
	SetAgentOffline(ctx context.Context, agentID string) error
	// RecordAgentChirp records the time of the most recent agent heartbeat.
	RecordAgentChirp(ctx context.Context, agentID string, at time.Time) error
	// CleanStaleAgents marks agents offline when their last heartbeat predates
	// cutoff.
	CleanStaleAgents(ctx context.Context, cutoff time.Time) error
}

// PairingCode is the subset of a one-time enrolment code the tunnel inspects.
type PairingCode struct {
	ID        string
	Used      bool
	ExpiresAt time.Time
}

// AgentRegistration carries the details recorded when an agent enrols.
type AgentRegistration struct {
	ID            string
	Hostname      string
	OS            string
	Arch          string
	PublicKey     string
	PairingCodeID string
	// TokenHash is HashCredential of the agent's reconnection token. The token
	// itself is handed to the agent once and never stored.
	TokenHash string
}

// AgentIdentity is the subset of a stored agent the tunnel uses to
// authenticate a reconnecting agent.
type AgentIdentity struct {
	ID        string
	Hostname  string
	OS        string
	Arch      string
	PublicKey string
	// TokenHash is what HandleConnect compares the presented token against. An
	// agent row that carries no hash cannot authenticate: see HandleConnect.
	TokenHash string
}
