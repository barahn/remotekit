//go:build windows

package input

import (
	"fmt"
	"image"
	"runtime"
)

// NewInjector creates a Windows input injector using SendInput API.
// TODO: Implement using SendInput API (Phase 1, Sprint 11-12).
func NewInjector() (Injector, error) {
	return nil, fmt.Errorf("%w: input injection not yet implemented for %s/%s",
		ErrNotImplemented, runtime.GOOS, runtime.GOARCH)
}

type windowsInjector struct{}

func (i *windowsInjector) SetScreenBounds(_ image.Rectangle) {}
func (i *windowsInjector) MoveMouse(_, _ float64) error      { return ErrNotImplemented }
func (i *windowsInjector) MouseDown(_ MouseButton, _, _ float64) error { return ErrNotImplemented }
func (i *windowsInjector) MouseUp(_ MouseButton, _, _ float64) error   { return ErrNotImplemented }
func (i *windowsInjector) Scroll(_, _, _, _ float64) error   { return ErrNotImplemented }
func (i *windowsInjector) KeyDown(_ KeyboardEvent) error    { return ErrNotImplemented }
func (i *windowsInjector) KeyUp(_ KeyboardEvent) error      { return ErrNotImplemented }
func (i *windowsInjector) Close() error                     { return nil }
