// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package clipboard

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
)

// DarwinClipboard implements system clipboard operations on macOS via pbcopy/pbpaste
// and implements Concealer to prevent password exfiltration (barahn/remotekit#130).
type DarwinClipboard struct {
	mu             sync.RWMutex
	memoryFallback string
	typesReader    func(ctx context.Context) ([]string, error)
}

var _ Concealer = (*DarwinClipboard)(nil)
var _ FileManager = (*DarwinClipboard)(nil)

// NewManager returns a new macOS clipboard manager.
func NewManager() Manager {
	return &DarwinClipboard{}
}

func (dc *DarwinClipboard) GetText(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "pbpaste")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		return out.String(), nil
	}

	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.memoryFallback, nil
}

func (dc *DarwinClipboard) SetText(ctx context.Context, text string) error {
	dc.mu.Lock()
	dc.memoryFallback = text
	dc.mu.Unlock()

	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run()
	return nil
}

// GetFiles retrieves the file list from the macOS clipboard (stubbed until native macOS lands).
func (dc *DarwinClipboard) GetFiles(ctx context.Context) (FileList, error) {
	return FileList{}, nil
}

// SetFiles writes the file list to the macOS clipboard (stubbed until native macOS lands).
func (dc *DarwinClipboard) SetFiles(ctx context.Context, list FileList) error {
	return nil
}

// Concealed reports whether the current clipboard content was marked as secret
// by a password manager (1Password, Bitwarden, KeePassXC, etc.).
func (dc *DarwinClipboard) Concealed(ctx context.Context) (bool, error) {
	readTypes := dc.typesReader
	if readTypes == nil {
		readTypes = defaultDarwinPasteboardTypes
	}
	types, err := readTypes(ctx)
	if err != nil {
		// If we cannot determine types, hold back rather than risk leaking secrets
		return true, err
	}
	return concealedByDarwinTypes(types), nil
}

func defaultDarwinPasteboardTypes(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e",
		"ObjC.import('AppKit'); try { var t = $.NSPasteboard.generalPasteboard.types; var r = []; for (var i = 0; i < t.count; i++) { r.push(ObjC.unwrap(t.objectAtIndex(i))); } r.join('\\n'); } catch(e) { '' }")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var types []string
	for _, line := range strings.Split(out.String(), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			types = append(types, trimmed)
		}
	}
	return types, nil
}
