// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package transfer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const ChunkSize = 64 * 1024 // 64 KB per chunk

var (
	ErrTransferNotFound = errors.New("file transfer session not found")
	ErrChecksumMismatch = errors.New("file integrity verification failed: SHA-256 checksum mismatch")
	ErrIncomplete       = errors.New("file transfer is incomplete: not every announced byte arrived")
)

type FileSession struct {
	ID             string
	FileName       string
	TotalSize      int64
	ReceivedBytes  int64
	Chunks         map[int][]byte
	ExpectedSHA256 string
	Completed      bool
}

type Manager struct {
	mu          sync.RWMutex
	downloadDir string
	sessions    map[string]*FileSession
}

func NewManager(downloadDir string) (*Manager, error) {
	if downloadDir == "" {
		// The system temporary directory is shared with other local users,
		// who could pre-create names or links in it. Use a private directory
		// with an unpredictable name (mode 0700, created exclusively).
		dir, err := os.MkdirTemp("", "remote-transfer-")
		if err != nil {
			return nil, fmt.Errorf("failed to create private download directory: %w", err)
		}
		downloadDir = dir
	} else if err := os.MkdirAll(downloadDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create download directory %s: %w", downloadDir, err)
	}

	return &Manager{
		downloadDir: downloadDir,
		sessions:    make(map[string]*FileSession),
	}, nil
}

func (m *Manager) StartSession(id, fileName string, totalSize int64, expectedSHA256 string) *FileSession {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Normalize Windows backslashes and strip directory components
	normalized := strings.ReplaceAll(fileName, "\\", "/")
	cleanBase := filepath.Base(normalized)
	if cleanBase == "." || cleanBase == "/" || cleanBase == "" {
		cleanBase = "download.bin"
	}

	session := &FileSession{
		ID:             id,
		FileName:       cleanBase,
		TotalSize:      totalSize,
		Chunks:         make(map[int][]byte),
		ExpectedSHA256: expectedSHA256,
	}
	m.sessions[id] = session
	return session
}

// Discard drops a transfer and everything received for it, without writing
// anything.
func (m *Manager) Discard(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// Describe returns the name a transfer will be saved under and the size it
// announced, or zero values for an unknown transfer.
func (m *Manager) Describe(id string) (name string, size int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		return s.FileName, s.TotalSize
	}
	return "", 0
}

func (m *Manager) AddChunkBase64(id string, index int, base64Data string) (float64, bool, error) {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return 0, false, fmt.Errorf("failed to decode chunk base64 data: %w", err)
	}
	return m.AddChunk(id, index, data)
}

func (m *Manager) AddChunk(id string, index int, data []byte) (float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[id]
	if !exists {
		return 0, false, ErrTransferNotFound
	}

	if _, exists := session.Chunks[index]; !exists {
		session.Chunks[index] = data
		session.ReceivedBytes += int64(len(data))
	}

	progress := float64(0)
	if session.TotalSize > 0 {
		progress = (float64(session.ReceivedBytes) / float64(session.TotalSize)) * 100.0
		if progress > 100.0 {
			progress = 100.0
		}
	}

	if session.ReceivedBytes >= session.TotalSize {
		session.Completed = true
	}

	return progress, session.Completed, nil
}

func (m *Manager) AssembleFile(id string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[id]
	if !exists {
		return "", "", ErrTransferNotFound
	}
	// A transfer is assembled once, whatever the outcome: a repeated
	// file_complete must not write another copy of the same data.
	delete(m.sessions, id)

	// Every announced byte, in chunks with no gap, or nothing is written.
	contiguous := int64(0)
	for i := 0; ; i++ {
		chunk, ok := session.Chunks[i]
		if !ok {
			break
		}
		contiguous += int64(len(chunk))
	}
	if contiguous != session.TotalSize || session.ReceivedBytes != session.TotalSize {
		return "", "", ErrIncomplete
	}

	outFile, destPath, err := createUnique(m.downloadDir, session.FileName)
	if err != nil {
		return "", "", err
	}
	// Guard for the early returns below; the success path closes explicitly
	// and checks the error.
	defer func() { _ = outFile.Close() }()

	hasher := sha256.New()

	for i := 0; ; i++ {
		chunk, exists := session.Chunks[i]
		if !exists {
			break
		}
		if _, err := outFile.Write(chunk); err != nil {
			return "", "", fmt.Errorf("failed to write chunk %d: %w", i, err)
		}
		hasher.Write(chunk)
	}

	calculatedSHA256 := hex.EncodeToString(hasher.Sum(nil))

	// Close before the checksum verdict. A write that only fails at close time
	// would otherwise be reported as a completed transfer, and Windows refuses
	// to remove a file that is still open.
	if err := outFile.Close(); err != nil {
		_ = os.Remove(destPath)
		return "", "", fmt.Errorf("failed to close destination file %s: %w", destPath, err)
	}

	if session.ExpectedSHA256 != "" && session.ExpectedSHA256 != calculatedSHA256 {
		_ = os.Remove(destPath)
		return "", "", ErrChecksumMismatch
	}

	return destPath, calculatedSHA256, nil
}

// maxNameAttempts bounds how many numbered names createUnique tries before
// giving up on a crowded directory.
const maxNameAttempts = 1000

// createUnique creates name in dir without replacing anything already there:
// a file of the same name gets a numbered sibling, "report (1).pdf". Received
// files may land in a directory the user keeps their own files in, and a
// sender must not be able to overwrite one by naming it.
func createUnique(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < maxNameAttempts; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- name is filepath.Base'd by StartSession
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("failed to create destination file %s: %w", path, err)
		}
	}
	return nil, "", fmt.Errorf("failed to create destination file for %s: too many files of that name", name)
}

func ChunkFile(filePath string) (string, int64, []string, string, error) {
	data, err := os.ReadFile(filePath) // #nosec G304 -- file path input for chunking
	if err != nil {
		return "", 0, nil, "", err
	}

	totalSize := int64(len(data))
	hasher := sha256.New()
	hasher.Write(data)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	var chunks []string
	for i := 0; i < len(data); i += ChunkSize {
		end := i + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		encoded := base64.StdEncoding.EncodeToString(data[i:end])
		chunks = append(chunks, encoded)
	}

	fileName := filepath.Base(filePath)
	return fileName, totalSize, chunks, checksum, nil
}
