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
		downloadDir = os.TempDir()
	}
	if err := os.MkdirAll(downloadDir, 0750); err != nil {
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

	destPath := filepath.Join(m.downloadDir, session.FileName)
	outFile, err := os.Create(destPath) // #nosec G304 -- download path validated via filepath.Base
	if err != nil {
		return "", "", fmt.Errorf("failed to create destination file %s: %w", destPath, err)
	}
	defer outFile.Close()

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

	if session.ExpectedSHA256 != "" && session.ExpectedSHA256 != calculatedSHA256 {
		_ = os.Remove(destPath)
		return "", "", ErrChecksumMismatch
	}

	return destPath, calculatedSHA256, nil
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
