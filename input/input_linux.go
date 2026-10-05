// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package input

import (
	"fmt"
	"image"
	"math"
	"os"
	"sync"

	"github.com/barahn/remotekit/internal/x11"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// X11 event types for xtest.FakeInput
const (
	xKeyPress      byte = 2
	xKeyRelease    byte = 3
	xButtonPress   byte = 4
	xButtonRelease byte = 5
	xMotionNotify  byte = 6
)

// linuxInjector implements Injector using pure Go X11 (XTest & WarpPointer).
type linuxInjector struct {
	mu     sync.Mutex
	conn   *xgb.Conn
	root   xproto.Window
	bounds image.Rectangle
}

// NewInjector creates a new platform-specific input injector.
// On Linux, this auto-detects Wayland vs X11 and returns the appropriate injector.
func NewInjector() (Injector, error) {
	// If X11/Xwayland DISPLAY is set, prioritize direct low-latency input via XTest
	if os.Getenv("DISPLAY") != "" {
		if inj, err := newX11Injector(); err == nil {
			return inj, nil
		}
	}

	// Under Wayland, use native Wayland RemoteDesktop injector (Mutter / Portal)
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		if winj, err := newWaylandInjector(); err == nil {
			return winj, nil
		}
	}

	// Fallback to X11
	if inj, err := newX11Injector(); err == nil {
		return inj, nil
	}

	// Fallback to Wayland RemoteDesktop
	if winj, werr := newWaylandInjector(); werr == nil {
		return winj, nil
	}

	return nil, fmt.Errorf("%w: no suitable input injector found", ErrDeviceNotFound)
}

func newX11Injector() (*linuxInjector, error) {
	conn, err := x11.NewConn()
	if err != nil {
		return nil, fmt.Errorf("%w: cannot connect to X11 display (is $DISPLAY set?): %v", ErrDeviceNotFound, err)
	}

	if err := x11.Enable(conn, x11.XTest); err != nil {
		conn.Close()
		return nil, fmt.Errorf("input: XTest extension unavailable: %w", err)
	}

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	inj := &linuxInjector{
		conn: conn,
		root: screen.Root,
		bounds: image.Rect(
			0, 0,
			int(screen.WidthInPixels),
			int(screen.HeightInPixels),
		),
	}

	return inj, nil
}

func (inj *linuxInjector) SetScreenBounds(bounds image.Rectangle) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	if !bounds.Empty() {
		inj.bounds = bounds
	}
}

// toPixels maps normalized (0.0-1.0) viewer coordinates onto the captured
// screen area, in the global root-window coordinates XTest warps to.
//
// The normalized pair addresses the frame the technician is looking at, which
// is one monitor, while XTest addresses the whole X screen. On a multi-monitor
// setup the captured monitor does not start at the origin -- a second monitor
// to the right of a 1920-wide primary starts at x=1920 -- so the offset has to
// be added back or every click on that monitor lands on the primary one.
func (inj *linuxInjector) toPixels(x, y float64) (int16, int16) {
	maxW := float64(inj.bounds.Dx() - 1)
	maxH := float64(inj.bounds.Dy() - 1)
	if maxW < 0 {
		maxW = 0
	}
	if maxH < 0 {
		maxH = 0
	}

	px := math.Round(x * maxW)
	py := math.Round(y * maxH)

	if px < 0 {
		px = 0
	} else if px > maxW {
		px = maxW
	}
	if py < 0 {
		py = 0
	} else if py > maxH {
		py = maxH
	}

	px += float64(inj.bounds.Min.X)
	py += float64(inj.bounds.Min.Y)

	return int16(px), int16(py)
}

func (inj *linuxInjector) MoveMouse(x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	px, py := inj.toPixels(x, y)

	// Synchronize internal X11 pointer position and dispatch MotionNotify
	_ = xproto.WarpPointer(inj.conn, 0, inj.root, 0, 0, 0, 0, px, py)
	return xtest.FakeInputChecked(inj.conn, xMotionNotify, 0, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) MouseDown(button MouseButton, x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	px, py := inj.toPixels(x, y)
	xButton := mapMouseButton(button)

	_ = xproto.WarpPointer(inj.conn, 0, inj.root, 0, 0, 0, 0, px, py)
	_ = xtest.FakeInputChecked(inj.conn, xMotionNotify, 0, 0, inj.root, px, py, 0).Check()
	return xtest.FakeInputChecked(inj.conn, xButtonPress, xButton, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) MouseUp(button MouseButton, x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	px, py := inj.toPixels(x, y)
	xButton := mapMouseButton(button)

	_ = xproto.WarpPointer(inj.conn, 0, inj.root, 0, 0, 0, 0, px, py)
	_ = xtest.FakeInputChecked(inj.conn, xMotionNotify, 0, 0, inj.root, px, py, 0).Check()
	return xtest.FakeInputChecked(inj.conn, xButtonRelease, xButton, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	px, py := inj.toPixels(x, y)

	// X11 mouse wheel buttons: 4 (scroll up), 5 (scroll down), 6 (scroll left), 7 (scroll right)
	var btn byte
	if deltaY < 0 {
		btn = 4 // Scroll up
	} else if deltaY > 0 {
		btn = 5 // Scroll down
	} else if deltaX < 0 {
		btn = 6 // Scroll left
	} else if deltaX > 0 {
		btn = 7 // Scroll right
	} else {
		return nil
	}

	_ = xproto.WarpPointer(inj.conn, 0, inj.root, 0, 0, 0, 0, px, py)
	_ = xtest.FakeInputChecked(inj.conn, xButtonPress, btn, 0, inj.root, px, py, 0).Check()
	return xtest.FakeInputChecked(inj.conn, xButtonRelease, btn, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) KeyDown(event KeyboardEvent) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	keycode := mapKeycode(event.Key, event.Code)
	if keycode == 0 {
		return nil
	}

	return xtest.FakeInputChecked(inj.conn, xKeyPress, keycode, 0, inj.root, 0, 0, 0).Check()
}

func (inj *linuxInjector) KeyUp(event KeyboardEvent) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	keycode := mapKeycode(event.Key, event.Code)
	if keycode == 0 {
		return nil
	}

	return xtest.FakeInputChecked(inj.conn, xKeyRelease, keycode, 0, inj.root, 0, 0, 0).Check()
}

func (inj *linuxInjector) Close() error {
	if inj == nil {
		return nil
	}
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn != nil {
		inj.conn.Close()
		inj.conn = nil
	}
	return nil
}

func mapMouseButton(button MouseButton) byte {
	switch button {
	case ButtonLeft:
		return 1
	case ButtonMiddle:
		return 2
	case ButtonRight:
		return 3
	default:
		return 1
	}
}

var domCodeToX11Keycode = map[string]byte{
	// Letter keys (QWERTY / ABNT2 physical layout evdev keycodes + 8)
	"KeyA": 38, "KeyB": 56, "KeyC": 54, "KeyD": 40, "KeyE": 26, "KeyF": 41,
	"KeyG": 42, "KeyH": 43, "KeyI": 31, "KeyJ": 44, "KeyK": 45, "KeyL": 46,
	"KeyM": 58, "KeyN": 57, "KeyO": 32, "KeyP": 33, "KeyQ": 24, "KeyR": 27,
	"KeyS": 39, "KeyT": 28, "KeyU": 30, "KeyV": 55, "KeyW": 25, "KeyX": 53,
	"KeyY": 29, "KeyZ": 52,

	// Number row keys
	"Digit1": 10, "Digit2": 11, "Digit3": 12, "Digit4": 13, "Digit5": 14,
	"Digit6": 15, "Digit7": 16, "Digit8": 17, "Digit9": 18, "Digit0": 19,

	// Action & Control keys
	"Enter": 36, "NumpadEnter": 104, "Escape": 9, "Backspace": 22, "Tab": 23, "Space": 65,
	"Minus": 20, "Equal": 21, "BracketLeft": 34, "BracketRight": 35, "Backslash": 51,
	"Semicolon": 47, "Quote": 48, "Backquote": 49, "Comma": 59, "Period": 60, "Slash": 61,
	"CapsLock": 66, "Capslock": 66,

	// Modifiers
	"ShiftLeft": 50, "ShiftRight": 62,
	"ControlLeft": 37, "ControlRight": 105,
	"AltLeft": 64, "AltRight": 108,
	"MetaLeft": 133, "MetaRight": 134,

	// Navigation & Arrow keys
	"ArrowUp": 111, "ArrowDown": 116, "ArrowLeft": 113, "ArrowRight": 114,
	"Home": 110, "End": 115, "PageUp": 112, "PageDown": 117,
	"Insert": 118, "Delete": 119,

	// Function keys
	"F1": 67, "F2": 68, "F3": 69, "F4": 70, "F5": 71, "F6": 72,
	"F7": 73, "F8": 74, "F9": 75, "F10": 76, "F11": 95, "F12": 96,

	// International / ABNT2 keys
	"IntlRo": 97, "IntlBackslash": 94, "SemicolonABNT2": 47,
}

var charToX11Keycode = map[rune]byte{
	'a': 38, 'b': 56, 'c': 54, 'd': 40, 'e': 26, 'f': 41, 'g': 42, 'h': 43,
	'i': 31, 'j': 44, 'k': 45, 'l': 46, 'm': 58, 'n': 57, 'o': 32, 'p': 33,
	'q': 24, 'r': 27, 's': 39, 't': 28, 'u': 30, 'v': 55, 'w': 25, 'x': 53,
	'y': 29, 'z': 52,
	'A': 38, 'B': 56, 'C': 54, 'D': 40, 'E': 26, 'F': 41, 'G': 42, 'H': 43,
	'I': 31, 'J': 44, 'K': 45, 'L': 46, 'M': 58, 'N': 57, 'O': 32, 'P': 33,
	'Q': 24, 'R': 27, 'S': 39, 'T': 28, 'U': 30, 'V': 55, 'W': 25, 'X': 53,
	'Y': 29, 'Z': 52,
	'1': 10, '2': 11, '3': 12, '4': 13, '5': 14, '6': 15, '7': 16, '8': 17, '9': 18, '0': 19,
}

// mapKeycode maps key identifiers and DOM code strings to X11 keycodes.
func mapKeycode(key, code string) byte {
	if kc, ok := domCodeToX11Keycode[code]; ok && kc > 0 {
		return kc
	}
	switch key {
	case "Enter", "Return":
		return 36
	case "Escape", "Esc":
		return 9
	case "Backspace":
		return 22
	case "Tab":
		return 23
	case "Space", " ":
		return 65
	case "Shift", "ShiftLeft":
		return 50
	case "Control", "ControlLeft":
		return 37
	case "Alt", "AltLeft":
		return 64
	case "Meta", "Super", "MetaLeft":
		return 133
	default:
		if len(key) == 1 {
			r := rune(key[0])
			if kc, ok := charToX11Keycode[r]; ok {
				return kc
			}
		}
		return 0
	}
}
