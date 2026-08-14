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
	defer mu.Unlock()

	if len(changes) != 2 {
		t.Fatalf("expected 2 callback invocations, got %d: %v", len(changes), changes)
	}

	if changes[0] != "First change" || changes[1] != "Second change" {
		t.Errorf("unexpected changes received: %v", changes)
	}
}
