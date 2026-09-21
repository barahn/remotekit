// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned by MemStore when a record does not exist.
var ErrNotFound = errors.New("not found")

// MemStore is an in-memory AgentStore for tests. It is safe for concurrent use
// and is shared with the external tunnel_test package.
type MemStore struct {
	mu           sync.Mutex
	pairingCodes map[string]*PairingCode
	agents       map[string]*AgentIdentity
	status       map[string]string
	lastChirp    map[string]time.Time
}

// NewMemStore returns an empty in-memory AgentStore.
func NewMemStore() *MemStore {
	return &MemStore{
		pairingCodes: make(map[string]*PairingCode),
		agents:       make(map[string]*AgentIdentity),
		status:       make(map[string]string),
		lastChirp:    make(map[string]time.Time),
	}
}

// SeedPairingCode registers a pairing code under the given hash.
func (m *MemStore) SeedPairingCode(codeHash string, pc PairingCode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pairingCodes[codeHash] = &pc
}

// Status reports the recorded status of an agent ("online", "offline" or "").
func (m *MemStore) Status(agentID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status[agentID]
}

func (m *MemStore) GetPairingCodeByHash(_ context.Context, codeHash string) (PairingCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pc, ok := m.pairingCodes[codeHash]
	if !ok {
		return PairingCode{}, ErrNotFound
	}
	return *pc, nil
}

func (m *MemStore) MarkPairingCodeUsed(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pc := range m.pairingCodes {
		if pc.ID == id {
			pc.Used = true
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemStore) CreateAgent(_ context.Context, agent AgentRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[agent.ID] = &AgentIdentity{
		ID:        agent.ID,
		Hostname:  agent.Hostname,
		OS:        agent.OS,
		Arch:      agent.Arch,
		PublicKey: agent.PublicKey,
	}
	m.status[agent.ID] = "online"
	return nil
}

func (m *MemStore) GetAgentByID(_ context.Context, agentID string) (AgentIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agent, ok := m.agents[agentID]
	if !ok {
		return AgentIdentity{}, ErrNotFound
	}
	return *agent, nil
}

func (m *MemStore) UpdateAgentInfo(_ context.Context, agentID, hostname, osName, arch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	agent, ok := m.agents[agentID]
	if !ok {
		return ErrNotFound
	}
	agent.Hostname, agent.OS, agent.Arch = hostname, osName, arch
	return nil
}

func (m *MemStore) SetAgentOnline(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[agentID] = "online"
	return nil
}

func (m *MemStore) SetAgentOffline(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[agentID] = "offline"
	return nil
}

func (m *MemStore) RecordAgentChirp(_ context.Context, agentID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastChirp[agentID] = at
	return nil
}

func (m *MemStore) CleanStaleAgents(_ context.Context, cutoff time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for agentID, seen := range m.lastChirp {
		if seen.Before(cutoff) {
			m.status[agentID] = "offline"
		}
	}
	return nil
}
