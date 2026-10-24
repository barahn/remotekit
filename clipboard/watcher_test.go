// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	cb := NewMemoryClipboard()
	watcher := NewWatcher(cb, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var changes []string

	go watcher.Start(ctx, func(text string) {
		mu.Lock()
		changes = append(changes, text)
		mu.Unlock()
	})

	time.Sleep(50 * time.Millisecond)

	// Update clipboard content
	_ = cb.SetText(ctx, "First change")
	time.Sleep(60 * time.Millisecond)

	// Update again
	_ = cb.SetText(ctx, "Second change")
	time.Sleep(60 * time.Millisecond)

	// Update with UpdateLastText to prevent callback
	watcher.UpdateLastText("Suppressed change")
	_ = cb.SetText(ctx, "Suppressed change")
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	if len(changes) != 2 {
		t.Fatalf("expected 2 callback invocations, got %d: %v", len(changes), changes)
	}
	if changes[0] != "First change" || changes[1] != "Second change" {
		t.Errorf("unexpected changes received: %v", changes)
	}
	mu.Unlock()
}

func TestWatcherSuppressionWindow(t *testing.T) {
	cb := NewMemoryClipboard()
	watcher := NewWatcher(cb, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var changes []string

	go watcher.Start(ctx, func(text string) {
		mu.Lock()
		changes = append(changes, text)
		mu.Unlock()
	})

	time.Sleep(30 * time.Millisecond)

	// Simulate incoming remote clipboard event: UpdateLastText called before SetText
	remoteText := "Remote Text to Suppress Echo"
	watcher.UpdateLastText(remoteText)
	_ = cb.SetText(ctx, remoteText)

	// Wait for several poll intervals to ensure suppression window prevents echo
	time.Sleep(80 * time.Millisecond)

	mu.Lock()
	if len(changes) != 0 {
		t.Fatalf("expected 0 callbacks due to suppression window, got %d: %v", len(changes), changes)
	}
	mu.Unlock()

	// New local change should trigger callback normally
	_ = cb.SetText(ctx, "Local New Text")
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 1 || changes[0] != "Local New Text" {
		t.Fatalf("expected 1 callback for 'Local New Text', got %v", changes)
	}
}

func TestWatcherFiles(t *testing.T) {
	cb := NewMemoryClipboard()
	watcher := NewWatcher(cb, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var fileChanges []FileList

	watcher.OnFiles(func(files FileList) {
		mu.Lock()
		fileChanges = append(fileChanges, files)
		mu.Unlock()
	})

	go watcher.Start(ctx, nil)

	time.Sleep(50 * time.Millisecond)

	// Set files on clipboard
	list1 := FileList{Files: []string{"/tmp/file1.txt"}, Cut: false}
	_ = cb.SetFiles(ctx, list1)
	time.Sleep(60 * time.Millisecond)

	// Set different files
	list2 := FileList{Files: []string{"/tmp/file2.txt", "/tmp/file3.txt"}, Cut: true}
	_ = cb.SetFiles(ctx, list2)
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	if len(fileChanges) != 2 {
		t.Fatalf("expected 2 file callbacks, got %d: %v", len(fileChanges), fileChanges)
	}
	if !fileChanges[0].Equal(list1) {
		t.Errorf("expected list1, got %v", fileChanges[0])
	}
	if !fileChanges[1].Equal(list2) {
		t.Errorf("expected list2, got %v", fileChanges[1])
	}
	mu.Unlock()
}

func TestWatcherFilesSuppressionWindow(t *testing.T) {
	cb := NewMemoryClipboard()
	watcher := NewWatcher(cb, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var fileChanges []FileList

	watcher.OnFiles(func(files FileList) {
		mu.Lock()
		fileChanges = append(fileChanges, files)
		mu.Unlock()
	})

	go watcher.Start(ctx, nil)

	time.Sleep(30 * time.Millisecond)

	// Remote files arrived: suppress echo
	remoteFiles := FileList{Files: []string{"/tmp/remote.txt"}, Cut: false}
	watcher.UpdateLastFiles(remoteFiles)
	_ = cb.SetFiles(ctx, remoteFiles)

	time.Sleep(80 * time.Millisecond)

	mu.Lock()
	if len(fileChanges) != 0 {
		t.Fatalf("expected 0 callbacks due to suppression window, got %d: %v", len(fileChanges), fileChanges)
	}
	mu.Unlock()

	// New local change should trigger callback
	localFiles := FileList{Files: []string{"/tmp/local.txt"}, Cut: true}
	_ = cb.SetFiles(ctx, localFiles)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fileChanges) != 1 || !fileChanges[0].Equal(localFiles) {
		t.Fatalf("expected 1 callback for localFiles, got %v", fileChanges)
	}
}
