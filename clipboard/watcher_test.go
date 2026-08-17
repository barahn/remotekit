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
