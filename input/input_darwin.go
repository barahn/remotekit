//go:build darwin

package input

import (
	"fmt"
	"image"
	"runtime"
)

// NewInjector creates a macOS input injector using CGEvent.
// TODO: Implement using CGEvent API (Phase 2).
func NewInjector() (Injector, error) {
	return nil, fmt.Errorf("%w: input injection not yet implemented for %s/%s",
		ErrNotImplemented, runtime.GOOS, runtime.GOARCH)
}

type darwinInjector struct{}

func (i *darwinInjector) SetScreenBounds(_ image.Rectangle) {}
func (i *darwinInjector) MoveMouse(_, _ float64) error      { return ErrNotImplemented }
func (i *darwinInjector) MouseDown(_ MouseButton, _, _ float64) error { return ErrNotImplemented }
func (i *darwinInjector) MouseUp(_ MouseButton, _, _ float64) error   { return ErrNotImplemented }
func (i *darwinInjector) Scroll(_, _, _, _ float64) error   { return ErrNotImplemented }
func (i *darwinInjector) KeyDown(_ KeyboardEvent) error    { return ErrNotImplemented }
func (i *darwinInjector) KeyUp(_ KeyboardEvent) error      { return ErrNotImplemented }
func (i *darwinInjector) Close() error                     { return nil }
