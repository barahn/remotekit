//go:build darwin

package clipboard

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
)

// DarwinClipboard implements system clipboard operations on macOS via pbcopy/pbpaste.
type DarwinClipboard struct {
	mu             sync.RWMutex
	memoryFallback string
}

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
