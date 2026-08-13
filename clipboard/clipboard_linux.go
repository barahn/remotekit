package clipboard

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
)

// LinuxClipboard implements system clipboard reading and writing on Linux (X11 & Wayland).
type LinuxClipboard struct {
	mu            sync.RWMutex
	memoryFallback string
}

func NewManager() Manager {
	return &LinuxClipboard{}
}

func (lc *LinuxClipboard) GetText(ctx context.Context) (string, error) {
	// 1. Try xclip (X11)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, path, "-selection", "clipboard", "-o") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			return out.String(), nil
		}
	}

	// 2. Try xsel (X11)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--clipboard", "--output") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			return out.String(), nil
		}
	}

	// 3. Try wl-paste (Wayland)
	if path, err := exec.LookPath("wl-paste"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--no-newline") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			return out.String(), nil
		}
	}

	// 4. Memory fallback
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.memoryFallback, nil
}

func (lc *LinuxClipboard) SetText(ctx context.Context, text string) error {
	lc.mu.Lock()
	lc.memoryFallback = text
	lc.mu.Unlock()

	var success bool

	// 1. Try xclip (X11)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, path, "-selection", "clipboard") // #nosec G204 -- path resolved via exec.LookPath
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			success = true
		}
	}

	// 2. Try xsel (X11)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--clipboard", "--input") // #nosec G204 -- path resolved via exec.LookPath
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			success = true
		}
	}

	// 3. Try wl-copy (Wayland)
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.CommandContext(ctx, path, text) // #nosec G204 -- path resolved via exec.LookPath
		if err := cmd.Run(); err == nil {
			success = true
		}
	}

	_ = success
	return nil
}
