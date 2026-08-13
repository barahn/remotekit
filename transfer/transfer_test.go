package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileTransfer(t *testing.T) {
	tempDir := t.TempDir()
	mgr, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("failed to create transfer manager: %v", err)
	}

	srcFile := filepath.Join(tempDir, "test.txt")
	content := []byte("Hello Barahn File Transfer! Chunking and SHA256 checksum testing.")
	if err := os.WriteFile(srcFile, content, 0600); err != nil {
		t.Fatalf("failed to write source test file: %v", err)
	}

	fileName, size, chunks, checksum, err := ChunkFile(srcFile)
	if err != nil {
		t.Fatalf("failed to chunk file: %v", err)
	}

	if fileName != "test.txt" {
		t.Errorf("expected filename 'test.txt', got %q", fileName)
	}

	if size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), size)
	}

	sessionID := "sess-123"
	mgr.StartSession(sessionID, fileName, size, checksum)

	for i, chunk := range chunks {
		progress, done, err := mgr.AddChunkBase64(sessionID, i, chunk)
		if err != nil {
			t.Fatalf("failed to add chunk %d: %v", i, err)
		}
		if i == len(chunks)-1 && !done {
			t.Errorf("expected transfer to complete on last chunk")
		}
		if progress <= 0 {
			t.Errorf("expected progress > 0, got %f", progress)
		}
	}

	outPath, calculatedSHA, err := mgr.AssembleFile(sessionID)
	if err != nil {
		t.Fatalf("failed to assemble file: %v", err)
	}

	if calculatedSHA != checksum {
		t.Errorf("checksum mismatch: expected %s, got %s", checksum, calculatedSHA)
	}

	assembledContent, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read assembled file: %v", err)
	}

	if string(assembledContent) != string(content) {
		t.Errorf("content mismatch: expected %q, got %q", string(content), string(assembledContent))
	}
}
