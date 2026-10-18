// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"errors"
	"sync"
)

var ErrClipboardUnavailable = errors.New("clipboard utility not available on host system")

// Manager defines the interface for cross-platform text clipboard operations.
type Manager interface {
	// GetText reads the current text content from the system clipboard.
	GetText(ctx context.Context) (string, error)
	// SetText writes the specified text content to the system clipboard.
	SetText(ctx context.Context, text string) error
}

// MemoryClipboard provides a thread-safe in-memory fallback clipboard implementation.
type MemoryClipboard struct {
	mu        sync.RWMutex
	content   string
	concealed bool
}

func NewMemoryClipboard() *MemoryClipboard {
	return &MemoryClipboard{}
}

func (m *MemoryClipboard) GetText(ctx context.Context) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.content, nil
}

func (m *MemoryClipboard) SetText(ctx context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content, m.concealed = text, false
	return nil
}

// SetConcealedText puts text on the clipboard marked as secret, as a password
// manager does.
func (m *MemoryClipboard) SetConcealedText(ctx context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content, m.concealed = text, true
	return nil
}

// Concealed reports whether the content was set with SetConcealedText.
func (m *MemoryClipboard) Concealed(ctx context.Context) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.concealed, nil
}
