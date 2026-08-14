package clipboard

import (
	"context"
	"sync"
	"time"
)

// Watcher continuously monitors host system clipboard for changes.
type Watcher struct {
	mgr      Manager
	interval time.Duration
	mu       sync.RWMutex
	lastText string
}

// NewWatcher creates a new clipboard watcher instance.
func NewWatcher(mgr Manager, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	return &Watcher{
		mgr:      mgr,
		interval: interval,
	}
}

// UpdateLastText updates the recorded last-seen text without triggering callbacks.
// This is used when remote clipboard events are applied locally to prevent reflection loops.
func (w *Watcher) UpdateLastText(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastText = text
}

// GetLastText returns the most recent clipboard content observed by the watcher.
func (w *Watcher) GetLastText() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastText
}

// Start begins the polling loop, invoking onChange whenever the clipboard content changes.
func (w *Watcher) Start(ctx context.Context, onChange func(text string)) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Initial read to seed lastText without broadcasting
	if initial, err := w.mgr.GetText(ctx); err == nil && initial != "" {
		w.mu.Lock()
		w.lastText = initial
		w.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := w.mgr.GetText(ctx)
			if err != nil || current == "" {
				continue
			}

			w.mu.Lock()
			if current != w.lastText {
				w.lastText = current
				w.mu.Unlock()
				if onChange != nil {
					onChange(current)
				}
			} else {
				w.mu.Unlock()
			}
		}
	}
}
