// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package input

import (
	"image"
	"math"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/barahn/remotekit/permission"
)

// This file is the macOS injector's logic: where a click lands, which events
// a key becomes, how clicks are counted. It has no build tag so that it is
// tested on every platform's CI against a fake poster; input_darwin.go posts
// the events for real, with CGEventPost.
//
// Keys: a printable character is typed as text (a Unicode string on the
// event), so what the viewer's keyboard produced is what arrives, accents
// included, whatever layout the Mac uses. Anything else -- named keys, and
// characters typed with Control, Option or Command held -- is posted by
// position, from the DOM code, so shortcuts work. Ctrl is Control and Meta is
// Command: the injector types what the viewer pressed, as the other backends
// do. A product that wants a Windows technician's Ctrl+C to copy on a Mac
// maps Ctrl to Meta in its viewer.

// macPoster posts events to the system.
type macPoster interface {
	// trusted reports whether the process holds Accessibility, without
	// which macOS drops posted events silently.
	trusted() bool
	// mainBounds is the main display in global points.
	mainBounds() image.Rectangle
	mouse(typ uint32, x, y float64, button uint32, clicks int64, flags uint64) error
	key(code uint16, down bool, flags uint64) error
	flagsChanged(code uint16, flags uint64) error
	text(s string, down bool) error
	scroll(vertical, horizontal int32) error
}

// CGEventType values for mouse events, and CGMouseButton values.
const (
	cgLeftMouseDown     uint32 = 1
	cgLeftMouseUp       uint32 = 2
	cgRightMouseDown    uint32 = 3
	cgRightMouseUp      uint32 = 4
	cgMouseMoved        uint32 = 5
	cgLeftMouseDragged  uint32 = 6
	cgRightMouseDragged uint32 = 7
	cgOtherMouseDown    uint32 = 25
	cgOtherMouseUp      uint32 = 26
	cgOtherMouseDragged uint32 = 27

	cgButtonLeft   uint32 = 0
	cgButtonRight  uint32 = 1
	cgButtonCenter uint32 = 2
)

// CGEventFlags bits.
const (
	cgFlagShift       uint64 = 1 << 17
	cgFlagControl     uint64 = 1 << 18
	cgFlagOption      uint64 = 1 << 19
	cgFlagCommand     uint64 = 1 << 20
	cgFlagNumericPad  uint64 = 1 << 21
	cgFlagSecondaryFn uint64 = 1 << 23
)

const (
	// macScrollLines is how far one wheel event scrolls.
	macScrollLines = 3
	// macDoubleClick is how close in time and place two presses must be to
	// count as one double click: macOS's default interval, and a few points.
	macDoubleClick      = 500 * time.Millisecond
	macDoubleClickSlack = 4.0
	// macTrustRecheck is how long an answer about Accessibility is reused.
	macTrustRecheck = time.Second
)

type macButton struct {
	cg                uint32
	down, up, dragged uint32
}

func macButtonFor(b MouseButton) macButton {
	switch b {
	case ButtonRight:
		return macButton{cgButtonRight, cgRightMouseDown, cgRightMouseUp, cgRightMouseDragged}
	case ButtonMiddle:
		return macButton{cgButtonCenter, cgOtherMouseDown, cgOtherMouseUp, cgOtherMouseDragged}
	default:
		return macButton{cgButtonLeft, cgLeftMouseDown, cgLeftMouseUp, cgLeftMouseDragged}
	}
}

type macInjector struct {
	p   macPoster
	now func() time.Time

	mu      sync.Mutex
	bounds  image.Rectangle
	held    uint64 // modifier flags from modifier keys held down
	pressed map[MouseButton]bool

	trustAt time.Time
	trustOK bool

	// The last press, for counting clicks.
	lastButton MouseButton
	lastAt     time.Time
	lastX      float64
	lastY      float64
	clicks     int64
}

func newMacInjector(p macPoster, now func() time.Time) *macInjector {
	return &macInjector{p: p, now: now, bounds: p.mainBounds(), pressed: map[MouseButton]bool{}}
}

// check returns the Accessibility error when the permission is not held.
func (i *macInjector) check() error {
	if t := i.now(); i.trustAt.IsZero() || t.Sub(i.trustAt) >= macTrustRecheck {
		i.trustOK, i.trustAt = i.p.trusted(), t
	}
	if !i.trustOK {
		return permission.Missing(ErrPermissionDenied, permission.Accessibility)
	}
	return nil
}

func (i *macInjector) SetScreenBounds(bounds image.Rectangle) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !bounds.Empty() {
		i.bounds = bounds
	}
}

// point maps a normalized position onto the streamed monitor, in global
// points. It stays fractional: on a Retina display half a point is a pixel.
func (i *macInjector) point(x, y float64) (float64, float64) {
	axis := func(min, span int, f float64) float64 {
		f = math.Max(0, math.Min(1, f))
		return float64(min) + f*float64(max(span-1, 0))
	}
	return axis(i.bounds.Min.X, i.bounds.Dx(), x), axis(i.bounds.Min.Y, i.bounds.Dy(), y)
}

func (i *macInjector) move(px, py float64) error {
	typ, button := cgMouseMoved, cgButtonLeft
	for _, b := range []MouseButton{ButtonLeft, ButtonRight, ButtonMiddle} {
		if i.pressed[b] {
			mb := macButtonFor(b)
			typ, button = mb.dragged, mb.cg
			break
		}
	}
	return i.p.mouse(typ, px, py, button, 0, i.held)
}

func (i *macInjector) MoveMouse(x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.check(); err != nil {
		return err
	}
	px, py := i.point(x, y)
	return i.move(px, py)
}

func (i *macInjector) MouseDown(button MouseButton, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.check(); err != nil {
		return err
	}
	px, py := i.point(x, y)
	t := i.now()
	if button == i.lastButton && i.clicks > 0 && t.Sub(i.lastAt) <= macDoubleClick &&
		math.Abs(px-i.lastX) <= macDoubleClickSlack && math.Abs(py-i.lastY) <= macDoubleClickSlack {
		i.clicks++
	} else {
		i.clicks = 1
	}
	i.lastButton, i.lastAt, i.lastX, i.lastY = button, t, px, py
	i.pressed[button] = true
	mb := macButtonFor(button)
	return i.p.mouse(mb.down, px, py, mb.cg, i.clicks, i.held)
}

func (i *macInjector) MouseUp(button MouseButton, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.check(); err != nil {
		return err
	}
	px, py := i.point(x, y)
	clicks := int64(1)
	if button == i.lastButton && i.clicks > 0 {
		clicks = i.clicks
	}
	i.lastAt = i.now()
	delete(i.pressed, button)
	mb := macButtonFor(button)
	return i.p.mouse(mb.up, px, py, mb.cg, clicks, i.held)
}

func (i *macInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.check(); err != nil {
		return err
	}
	lines := func(d float64) int32 {
		switch {
		case d > 0:
			return -macScrollLines
		case d < 0:
			return macScrollLines
		}
		return 0
	}
	v, h := lines(deltaY), lines(deltaX)
	if v == 0 && h == 0 {
		return nil
	}
	// The wheel event goes to whatever is under the pointer.
	px, py := i.point(x, y)
	if err := i.move(px, py); err != nil {
		return err
	}
	return i.p.scroll(v, h)
}

func (i *macInjector) KeyDown(event KeyboardEvent) error { return i.keyEvent(event, true) }

func (i *macInjector) KeyUp(event KeyboardEvent) error { return i.keyEvent(event, false) }

func (i *macInjector) keyEvent(event KeyboardEvent, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.check(); err != nil {
		return err
	}
	switch event.Key {
	case "Dead", "Process":
		// The composed character arrives with the next key.
		return nil
	}

	if code, flag, ok := macModifier(event); ok {
		if down {
			i.held |= flag
		} else {
			i.held &^= flag
		}
		return i.p.flagsChanged(code, i.held)
	}

	if text, ok := i.macText(event); ok {
		return i.p.text(text, down)
	}

	k, ok := macKeyByCode[event.Code]
	if !ok {
		k, ok = macKeyByName(event.Key)
	}
	if !ok {
		return nil
	}
	return i.p.key(k.code, down, i.held|eventFlags(event)|k.flags)
}

// macText returns the character to type as text, if the event is one.
// Characters typed with Control, Option or Command held are shortcuts and go
// by position instead, except outside ASCII: those are never shortcuts, and
// AltGr on a Windows viewer reports Ctrl and Alt held.
func (i *macInjector) macText(event KeyboardEvent) (string, bool) {
	r, size := utf8.DecodeRuneInString(event.Key)
	if r == utf8.RuneError || size != len(event.Key) || !unicode.IsPrint(r) || r == ' ' {
		return "", false
	}
	shortcut := event.Ctrl || event.Alt || event.Meta || i.held&(cgFlagControl|cgFlagOption|cgFlagCommand) != 0
	if shortcut && r < utf8.RuneSelf {
		return "", false
	}
	return event.Key, true
}

func eventFlags(event KeyboardEvent) uint64 {
	var f uint64
	if event.Shift {
		f |= cgFlagShift
	}
	if event.Ctrl {
		f |= cgFlagControl
	}
	if event.Alt {
		f |= cgFlagOption
	}
	if event.Meta {
		f |= cgFlagCommand
	}
	return f
}

// macModifier reports whether the event is a modifier key itself, with its
// key code and flag.
func macModifier(event KeyboardEvent) (uint16, uint64, bool) {
	switch event.Code {
	case "ShiftLeft":
		return 56, cgFlagShift, true
	case "ShiftRight":
		return 60, cgFlagShift, true
	case "ControlLeft":
		return 59, cgFlagControl, true
	case "ControlRight":
		return 62, cgFlagControl, true
	case "AltLeft":
		return 58, cgFlagOption, true
	case "AltRight":
		return 61, cgFlagOption, true
	case "MetaLeft", "OSLeft":
		return 55, cgFlagCommand, true
	case "MetaRight", "OSRight":
		return 54, cgFlagCommand, true
	case "":
		switch event.Key {
		case "Shift":
			return 56, cgFlagShift, true
		case "Control":
			return 59, cgFlagControl, true
		case "Alt":
			return 58, cgFlagOption, true
		case "Meta", "Super", "OS":
			return 55, cgFlagCommand, true
		}
	}
	return 0, 0, false
}

// macKey is a macOS virtual key code (kVK_*) and the flags the hardware sets
// with it: the arrows report the numeric pad and Fn, the other navigation and
// function keys Fn.
type macKey struct {
	code  uint16
	flags uint64
}

const fnFlags = cgFlagSecondaryFn
const arrowFlags = cgFlagNumericPad | cgFlagSecondaryFn

// macKeyByCode maps DOM KeyboardEvent.code values to macOS key positions
// (ANSI layout names, as the DOM uses).
var macKeyByCode = map[string]macKey{
	"KeyA": {0, 0}, "KeyS": {1, 0}, "KeyD": {2, 0}, "KeyF": {3, 0}, "KeyH": {4, 0}, "KeyG": {5, 0},
	"KeyZ": {6, 0}, "KeyX": {7, 0}, "KeyC": {8, 0}, "KeyV": {9, 0}, "KeyB": {11, 0}, "KeyQ": {12, 0},
	"KeyW": {13, 0}, "KeyE": {14, 0}, "KeyR": {15, 0}, "KeyY": {16, 0}, "KeyT": {17, 0}, "KeyO": {31, 0},
	"KeyU": {32, 0}, "KeyI": {34, 0}, "KeyP": {35, 0}, "KeyL": {37, 0}, "KeyJ": {38, 0}, "KeyK": {40, 0},
	"KeyN": {45, 0}, "KeyM": {46, 0},

	"Digit1": {18, 0}, "Digit2": {19, 0}, "Digit3": {20, 0}, "Digit4": {21, 0}, "Digit5": {23, 0},
	"Digit6": {22, 0}, "Digit7": {26, 0}, "Digit8": {28, 0}, "Digit9": {25, 0}, "Digit0": {29, 0},

	"Equal": {24, 0}, "Minus": {27, 0}, "BracketRight": {30, 0}, "BracketLeft": {33, 0}, "Quote": {39, 0},
	"Semicolon": {41, 0}, "Backslash": {42, 0}, "Comma": {43, 0}, "Slash": {44, 0}, "Period": {47, 0},
	"Backquote": {50, 0}, "IntlBackslash": {10, 0},

	"Enter": {36, 0}, "Tab": {48, 0}, "Space": {49, 0}, "Backspace": {51, 0}, "Escape": {53, 0},
	"CapsLock": {57, 0}, "Capslock": {57, 0},

	"F1": {122, fnFlags}, "F2": {120, fnFlags}, "F3": {99, fnFlags}, "F4": {118, fnFlags},
	"F5": {96, fnFlags}, "F6": {97, fnFlags}, "F7": {98, fnFlags}, "F8": {100, fnFlags},
	"F9": {101, fnFlags}, "F10": {109, fnFlags}, "F11": {103, fnFlags}, "F12": {111, fnFlags},

	"Insert": {114, fnFlags}, // Help, where a Mac keyboard has Insert
	"Home":   {115, fnFlags}, "PageUp": {116, fnFlags}, "Delete": {117, fnFlags},
	"End": {119, fnFlags}, "PageDown": {121, fnFlags},
	"ArrowLeft": {123, arrowFlags}, "ArrowRight": {124, arrowFlags},
	"ArrowDown": {125, arrowFlags}, "ArrowUp": {126, arrowFlags},

	"NumpadDecimal": {65, cgFlagNumericPad}, "NumpadMultiply": {67, cgFlagNumericPad},
	"NumpadAdd": {69, cgFlagNumericPad}, "NumLock": {71, cgFlagNumericPad},
	"NumpadDivide": {75, cgFlagNumericPad}, "NumpadEnter": {76, cgFlagNumericPad},
	"NumpadSubtract": {78, cgFlagNumericPad}, "NumpadEqual": {81, cgFlagNumericPad},
	"Numpad0": {82, cgFlagNumericPad}, "Numpad1": {83, cgFlagNumericPad}, "Numpad2": {84, cgFlagNumericPad},
	"Numpad3": {85, cgFlagNumericPad}, "Numpad4": {86, cgFlagNumericPad}, "Numpad5": {87, cgFlagNumericPad},
	"Numpad6": {88, cgFlagNumericPad}, "Numpad7": {89, cgFlagNumericPad}, "Numpad8": {91, cgFlagNumericPad},
	"Numpad9": {92, cgFlagNumericPad},
}

// macKeyByName maps a DOM KeyboardEvent.key to a position, for viewers that
// send no code. A single ASCII character goes to its US-layout position.
func macKeyByName(key string) (macKey, bool) {
	names := map[string]string{
		"Enter": "Enter", "Return": "Enter", "Tab": "Tab", " ": "Space", "Space": "Space",
		"Backspace": "Backspace", "Escape": "Escape", "Esc": "Escape", "Delete": "Delete",
		"Insert": "Insert", "Home": "Home", "End": "End", "PageUp": "PageUp", "PageDown": "PageDown",
		"ArrowLeft": "ArrowLeft", "ArrowRight": "ArrowRight", "ArrowUp": "ArrowUp", "ArrowDown": "ArrowDown",
		"CapsLock": "CapsLock",
		"-":        "Minus", "=": "Equal", "[": "BracketLeft", "]": "BracketRight", "\\": "Backslash",
		";": "Semicolon", "'": "Quote", ",": "Comma", ".": "Period", "/": "Slash", "`": "Backquote",
	}
	if code, ok := names[key]; ok {
		return macKeyByCode[code], true
	}
	if len(key) == 2 || len(key) == 3 {
		if k, ok := macKeyByCode[key]; ok && key[0] == 'F' { // F1..F12
			return k, true
		}
	}
	if len(key) == 1 {
		c := key[0]
		switch {
		case c >= 'a' && c <= 'z':
			return macKeyByCode["Key"+string(c-'a'+'A')], true
		case c >= 'A' && c <= 'Z':
			return macKeyByCode["Key"+key], true
		case c >= '0' && c <= '9':
			return macKeyByCode["Digit"+key], true
		}
	}
	return macKey{}, false
}

func (i *macInjector) Close() error { return nil }
