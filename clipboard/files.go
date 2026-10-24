// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
)

// FileList represents a list of local file paths on the clipboard.
type FileList struct {
	Files []string // absolute file paths
	Cut   bool     // true if the action was cut/move, false for copy
}

// IsEmpty reports whether the file list contains no files.
func (fl FileList) IsEmpty() bool {
	return len(fl.Files) == 0
}

// Equal reports whether fl and other have identical files and cut flag.
func (fl FileList) Equal(other FileList) bool {
	if fl.Cut != other.Cut || len(fl.Files) != len(other.Files) {
		return false
	}
	for i := range fl.Files {
		if fl.Files[i] != other.Files[i] {
			return false
		}
	}
	return true
}

// FileManager is an optional interface implemented by clipboard managers
// that support reading and writing lists of files (e.g. copied from file managers).
type FileManager interface {
	GetFiles(ctx context.Context) (FileList, error)
	SetFiles(ctx context.Context, list FileList) error
}

// GetFiles retrieves the current file list from the in-memory clipboard.
func (m *MemoryClipboard) GetFiles(ctx context.Context) (FileList, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make([]string, len(m.files.Files))
	copy(copied, m.files.Files)
	return FileList{Files: copied, Cut: m.files.Cut}, nil
}

// SetFiles stores the given file list in the in-memory clipboard.
func (m *MemoryClipboard) SetFiles(ctx context.Context, list FileList) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]string, len(list.Files))
	copy(copied, list.Files)
	m.files = FileList{Files: copied, Cut: list.Cut}
	m.concealed = false
	return nil
}

// SetConcealedFiles stores the given file list marked as secret in the in-memory clipboard.
func (m *MemoryClipboard) SetConcealedFiles(ctx context.Context, list FileList) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]string, len(list.Files))
	copy(copied, list.Files)
	m.files = FileList{Files: copied, Cut: list.Cut}
	m.concealed = true
	return nil
}
