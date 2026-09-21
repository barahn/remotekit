// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package transfer

import (
	"os"
	"path/filepath"
	"strings"
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

func TestFileTransfer_PathTraversalPrevention(t *testing.T) {
	tempDir := t.TempDir()
	mgr, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("failed to create transfer manager: %v", err)
	}

	// Attempt to start sessions with malicious filenames
	traversalNames := []struct {
		malicious string
		expected  string
	}{
		{"../../etc/passwd", "passwd"},
		{"../../../tmp/evil.sh", "evil.sh"},
		{"/etc/shadow", "shadow"},
		{"..\\..\\windows\\system32\\cmd.exe", "cmd.exe"},
		{"foo/../../bar.txt", "bar.txt"},
		{"normal.txt", "normal.txt"},
	}

	for _, tc := range traversalNames {
		sess := mgr.StartSession("sess-"+tc.expected, tc.malicious, 5, "abc123")
		if sess.FileName != tc.expected {
			t.Errorf("Path traversal NOT prevented: input=%q expected=%q got=%q", tc.malicious, tc.expected, sess.FileName)
		}
	}

	// Verify assembled file stays within download directory
	mgr.StartSession("sess-assemble-traversal", "../../escape.txt", 5, "")
	// Explicitly test AssembleFile with ../../etc/passwd as input
	mgr.StartSession("sess-passwd-traversal", "../../etc/passwd", 12, "")
	mgr.sessions["sess-passwd-traversal"].Chunks[0] = []byte("root:x:0:0::")
	mgr.sessions["sess-passwd-traversal"].ReceivedBytes = 12
	mgr.sessions["sess-passwd-traversal"].Completed = true

	passwdOutPath, _, err := mgr.AssembleFile("sess-passwd-traversal")
	if err != nil {
		t.Fatalf("AssembleFile failed for passwd traversal: %v", err)
	}

	absDir, _ := filepath.Abs(tempDir)
	absPasswdOut, _ := filepath.Abs(passwdOutPath)
	relPasswd, err := filepath.Rel(absDir, absPasswdOut)
	if err != nil || strings.HasPrefix(relPasswd, "..") {
		t.Fatalf("CRITICAL SECURITY GAP: ../../etc/passwd escaped download dir %q (out: %q, rel: %s)", absDir, absPasswdOut, relPasswd)
	}
	expectedPasswd := filepath.Join(tempDir, "passwd")
	if passwdOutPath != expectedPasswd {
		t.Errorf("Expected sanitized output %q, got %q", expectedPasswd, passwdOutPath)
	}
}
