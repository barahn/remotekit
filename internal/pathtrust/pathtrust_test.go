// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pathtrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyPath_NonRoot(t *testing.T) {
	if privileged() {
		t.Skip("skipping non-root test when running as root")
	}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "agent.json")

	// Missing file is acceptable
	if err := VerifyPath(filePath); err != nil {
		t.Errorf("VerifyPath(missing): want nil, got %v", err)
	}

	// Existing file is acceptable
	if err := os.WriteFile(filePath, []byte(`{"test":true}`), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := VerifyPath(filePath); err != nil {
		t.Errorf("VerifyPath(existing): want nil, got %v", err)
	}
}

func TestReadFile_NonRoot(t *testing.T) {
	if privileged() {
		t.Skip("skipping non-root test when running as root")
	}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.json")
	content := []byte(`{"key":"value"}`)
	if err := os.WriteFile(filePath, content, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	data, err := ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("ReadFile content mismatch: got %q, want %q", string(data), string(content))
	}

	// Non-existent file
	if _, err := ReadFile(filepath.Join(dir, "nonexistent.json")); err == nil {
		t.Error("ReadFile(nonexistent): want error, got nil")
	}
}
