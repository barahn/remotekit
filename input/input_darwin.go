// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package input

import (
	"errors"
	"fmt"
	"image"
	"time"
	"unicode/utf16"

	"github.com/barahn/remotekit/internal/macos"
	"github.com/barahn/remotekit/permission"
)

// NewInjector creates a macOS input injector that posts CGEvents.
//
// Posting needs the Accessibility permission. NewInjector succeeds without
// it, so a grant made during a session takes effect without a new injector;
// until then every call returns an error matching ErrPermissionDenied and
// permission.ErrNotGranted (see permission.NotGrantedError).
//
// Coordinates are mapped onto SetScreenBounds' rectangle in points, the space
// the screen capturer reports Frame.Bounds in. See input_macos.go for how keys
// are mapped.
func NewInjector() (Injector, error) {
	if err := macos.Load(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImplemented, err)
	}
	return newMacInjector(cgPoster{}, time.Now), nil
}

const (
	cgHIDEventTap          = 0
	cgEventFlagsChanged    = 12
	cgMouseEventClickState = 1
	cgScrollEventUnitLine  = 1
)

var errEventNotCreated = errors.New("input: CoreGraphics did not create the event")

type cgPoster struct{}

func (cgPoster) trusted() bool {
	return permission.Check(permission.Accessibility) == permission.Granted
}

func (cgPoster) mainBounds() image.Rectangle {
	r := macos.CGDisplayBounds(macos.CGMainDisplayID())
	return image.Rect(int(r.Origin.X), int(r.Origin.Y), int(r.Origin.X+r.Size.Width), int(r.Origin.Y+r.Size.Height))
}

// post sets the flags on ev, posts it and releases it.
func post(ev uintptr, flags uint64) error {
	if ev == 0 {
		return errEventNotCreated
	}
	defer macos.CFRelease(ev)
	macos.CGEventSetFlags(ev, flags)
	macos.CGEventPost(cgHIDEventTap, ev)
	return nil
}

func (cgPoster) mouse(typ uint32, x, y float64, button uint32, clicks int64, flags uint64) error {
	ev := macos.CGEventCreateMouseEvent(0, typ, macos.CGPoint{X: x, Y: y}, button)
	if ev != 0 && clicks > 0 {
		macos.CGEventSetIntegerValueField(ev, cgMouseEventClickState, clicks)
	}
	return post(ev, flags)
}

func (cgPoster) key(code uint16, down bool, flags uint64) error {
	return post(macos.CGEventCreateKeyboardEvent(0, code, down), flags)
}

func (cgPoster) flagsChanged(code uint16, flags uint64) error {
	ev := macos.CGEventCreateKeyboardEvent(0, code, true)
	if ev != 0 {
		macos.CGEventSetType(ev, cgEventFlagsChanged)
	}
	return post(ev, flags)
}

func (cgPoster) text(s string, down bool) error {
	units := utf16.Encode([]rune(s))
	if len(units) == 0 {
		return nil
	}
	// Key code 0 with a Unicode string: applications read the string.
	ev := macos.CGEventCreateKeyboardEvent(0, 0, down)
	if ev != 0 {
		macos.CGEventKeyboardSetUnicodeString(ev, uint64(len(units)), &units[0])
	}
	// No modifier flags: the string is the whole of what was typed.
	return post(ev, 0)
}

func (cgPoster) scroll(vertical, horizontal int32) error {
	return post(macos.CGEventCreateScrollWheelEvent2(0, cgScrollEventUnitLine, 2, vertical, horizontal, 0), 0)
}
