//go:build windows

package screen

import (
	"context"
	"fmt"
	"runtime"
)

// NewCapturer creates a Windows DXGI-based screen capturer.
// TODO: Implement using DXGI Desktop Duplication API (Phase 1, Sprint 11-12).
func NewCapturer(config CaptureConfig) (Capturer, error) {
	return nil, fmt.Errorf("%w: screen capture not yet implemented for %s/%s",
		ErrCaptureNotReady, runtime.GOOS, runtime.GOARCH)
}

// windowsCapturer is a placeholder for the DXGI implementation.
type windowsCapturer struct{}

func (c *windowsCapturer) Displays() ([]Display, error)       { return nil, ErrCaptureNotReady }
func (c *windowsCapturer) Start(_ context.Context) error       { return ErrCaptureNotReady }
func (c *windowsCapturer) Frames() <-chan *Frame               { return nil }
func (c *windowsCapturer) Stop()                               {}
func (c *windowsCapturer) SetDisplay(_ int) error              { return ErrCaptureNotReady }
func (c *windowsCapturer) Close()                              {}
