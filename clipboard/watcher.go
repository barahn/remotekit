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
	mgr                  Manager
	interval             time.Duration
	mu                   sync.RWMutex
	lastText             string
	suppressedText       string
	suppressedUntil      time.Time
	lastFiles            FileList
	suppressedFiles      FileList
	suppressedFilesUntil time.Time
	onConcealed          func()
	onFiles              func(FileList)
}

// OnConcealed registers fn to be called, instead of onChange, when the
// clipboard changes to content marked as secret (Concealer). Call it before
// Start.
func (w *Watcher) OnConcealed(fn func()) { w.onConcealed = fn }

// OnFiles registers fn to be called when the clipboard changes to a list of files
// (FileManager). Call it before Start.
func (w *Watcher) OnFiles(fn func(FileList)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onFiles = fn
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

// UpdateLastFiles updates the recorded last-seen file list without triggering callbacks.
// A 2-second suppression window is also established for this exact payload.
func (w *Watcher) UpdateLastFiles(files FileList) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastFiles = files
	w.suppressedFiles = files
	w.suppressedFilesUntil = time.Now().Add(2 * time.Second)
}

// GetLastFiles returns the most recent file list observed by the watcher.
func (w *Watcher) GetLastFiles() FileList {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastFiles
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

	seededText := false
	if initial, err := w.mgr.GetText(ctx); err == nil {
		w.mu.Lock()
		w.lastText = initial
		w.mu.Unlock()
		seededText = true
	}

	fileMgr, hasFiles := w.mgr.(FileManager)
	seededFiles := false
	if hasFiles {
		if initialFiles, err := fileMgr.GetFiles(ctx); err == nil {
			w.mu.Lock()
			w.lastFiles = initialFiles
			w.mu.Unlock()
			seededFiles = true
		}
	}

	concealer, _ := w.mgr.(Concealer)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Poll files if manager supports it
			if hasFiles {
				currentFiles, err := fileMgr.GetFiles(ctx)
				if err == nil {
					if !seededFiles {
						w.mu.Lock()
						w.lastFiles = currentFiles
						w.mu.Unlock()
						seededFiles = true
						continue
					}
					if len(currentFiles.Files) > 0 {
						w.mu.Lock()
						isSuppressed := currentFiles.Equal(w.suppressedFiles) && time.Now().Before(w.suppressedFilesUntil)
						isSame := currentFiles.Equal(w.lastFiles)
						if !isSuppressed && !isSame {
							w.lastFiles = currentFiles
						}
						onFilesFn := w.onFiles
						w.mu.Unlock()

						if !isSuppressed && !isSame {
							if concealer != nil {
								if hidden, err := concealer.Concealed(ctx); err != nil || hidden {
									if w.onConcealed != nil {
										w.onConcealed()
									}
									continue
								}
							}
							if onFilesFn != nil {
								onFilesFn(currentFiles)
							}
						}
					} else {
						w.mu.Lock()
						w.lastFiles = FileList{}
						w.mu.Unlock()
					}
				}
			}

			// Poll text
			current, err := w.mgr.GetText(ctx)
			if err != nil {
				continue
			}
			if !seededText {
				w.mu.Lock()
				w.lastText = current
				w.mu.Unlock()
				seededText = true
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
