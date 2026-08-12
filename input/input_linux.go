//go:build linux

package input

import (
	"fmt"
	"image"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// X11 event types for xtest.FakeInput
const (
	xKeyPress   byte = 2
	xKeyRelease byte = 3
	xButtonPress byte = 4
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
// On Linux, this connects to the X11 display server via pure Go xgb/xtest.
func NewInjector() (Injector, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("%w: cannot connect to X11 display (is $DISPLAY set?): %v", ErrDeviceNotFound, err)
	}

	if err := xtest.Init(conn); err != nil {
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

func (inj *linuxInjector) MoveMouse(x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.conn == nil {
		return ErrDeviceNotFound
	}

	px := int16(x * float64(inj.bounds.Dx()))
	py := int16(y * float64(inj.bounds.Dy()))

	// Warp mouse pointer to target screen coordinates
	return xproto.WarpPointerChecked(
		inj.conn,
		xproto.WindowNone,
		inj.root,
		0, 0, 0, 0,
		px, py,
	).Check()
}

func (inj *linuxInjector) MouseDown(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	px := int16(x * float64(inj.bounds.Dx()))
	py := int16(y * float64(inj.bounds.Dy()))
	xButton := mapMouseButton(button)
	return xtest.FakeInputChecked(inj.conn, xButtonPress, xButton, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) MouseUp(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	px := int16(x * float64(inj.bounds.Dx()))
	py := int16(y * float64(inj.bounds.Dy()))
	xButton := mapMouseButton(button)
	return xtest.FakeInputChecked(inj.conn, xButtonRelease, xButton, 0, inj.root, px, py, 0).Check()
}

func (inj *linuxInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	px := int16(x * float64(inj.bounds.Dx()))
	py := int16(y * float64(inj.bounds.Dy()))

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
			ch := key[0]
			if ch >= 'a' && ch <= 'z' {
				return byte(38 + (ch - 'a'))
			}
			if ch >= 'A' && ch <= 'Z' {
				return byte(38 + (ch - 'A'))
			}
			if ch >= '1' && ch <= '9' {
				return byte(10 + (ch - '1'))
			}
			if ch == '0' {
				return 19
			}
		}
		return 0
	}
}
