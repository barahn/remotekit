// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"sync"
	"time"
)

// Watcher continuously monitors host system clipboard for changes.
type Watcher struct {
	mgr             Manager
	interval        time.Duration
	mu              sync.RWMutex
	lastText        string
	suppressedText  string
	suppressedUntil time.Time
	onConcealed     func()
}

// OnConcealed registers fn to be called, instead of onChange, when the
// clipboard changes to content marked as secret (Concealer). Call it before
// Start.
func (w *Watcher) OnConcealed(fn func()) { w.onConcealed = fn }

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
// A 2-second suppression window is also established for this exact payload.
func (w *Watcher) UpdateLastText(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastText = text
	w.suppressedText = text
	w.suppressedUntil = time.Now().Add(2 * time.Second)
}

// GetLastText returns the most recent clipboard content observed by the watcher.
func (w *Watcher) GetLastText() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastText
}

// Start begins the polling loop, invoking onChange whenever the clipboard
// content changes.
//
// The first content it manages to read is the baseline, never a change, even
// when the clipboard could not be read at the start: what was copied before
// the watch began is not handed on. A change to content the Manager reports as
// concealed (Concealer) calls the OnConcealed callback instead of onChange.
func (w *Watcher) Start(ctx context.Context, onChange func(text string)) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	seeded := false
	if initial, err := w.mgr.GetText(ctx); err == nil {
		w.mu.Lock()
		w.lastText = initial
		w.mu.Unlock()
		seeded = true
	}
	concealer, _ := w.mgr.(Concealer)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := w.mgr.GetText(ctx)
			if err != nil {
				continue
			}
			if !seeded {
				w.mu.Lock()
				w.lastText = current
				w.mu.Unlock()
				seeded = true
				continue
			}
			if current == "" {
				continue
			}

			w.mu.Lock()
			// Check suppression window
			if current == w.suppressedText && time.Now().Before(w.suppressedUntil) {
				w.lastText = current
				w.mu.Unlock()
				continue
			}
			if current == w.lastText {
				w.mu.Unlock()
				continue
			}
			w.lastText = current
			w.mu.Unlock()

			if concealer != nil {
				if hidden, err := concealer.Concealed(ctx); err != nil || hidden {
					if w.onConcealed != nil {
						w.onConcealed()
					}
					continue
				}
			}
			if onChange != nil {
				onChange(current)
			}
		}
	}
}
