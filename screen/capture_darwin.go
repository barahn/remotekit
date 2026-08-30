//go:build darwin

package screen

import (
	"context"
	"fmt"
	"runtime"
)

// NewCapturer creates a macOS CGDisplay-based screen capturer.
// TODO: Implement using CGDisplayStream / ScreenCaptureKit (Phase 2).
func NewCapturer(config CaptureConfig) (Capturer, error) {
	return nil, fmt.Errorf("%w: screen capture not yet implemented for %s/%s",
		ErrCaptureNotReady, runtime.GOOS, runtime.GOARCH)
}

// darwinCapturer is a placeholder for the CGDisplayStream implementation.
type darwinCapturer struct{}

func (c *darwinCapturer) Displays() ([]Display, error)  { return nil, ErrCaptureNotReady }
func (c *darwinCapturer) Start(_ context.Context) error { return ErrCaptureNotReady }
func (c *darwinCapturer) Frames() <-chan *Frame         { return nil }
func (c *darwinCapturer) Stop()                         {}
func (c *darwinCapturer) SetDisplay(_ int) error        { return ErrCaptureNotReady }
func (c *darwinCapturer) Close()                        {}
