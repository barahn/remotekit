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

	xButton := mapMouseButton(button)
	return xtest.FakeInputChecked(inj.conn, xButtonPress, xButton, 0, inj.root, 0, 0, 0).Check()
}

func (inj *linuxInjector) MouseUp(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	xButton := mapMouseButton(button)
	return xtest.FakeInputChecked(inj.conn, xButtonRelease, xButton, 0, inj.root, 0, 0, 0).Check()
}

func (inj *linuxInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

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

	_ = xtest.FakeInputChecked(inj.conn, xButtonPress, btn, 0, inj.root, 0, 0, 0).Check()
	return xtest.FakeInputChecked(inj.conn, xButtonRelease, btn, 0, inj.root, 0, 0, 0).Check()
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

// mapKeycode maps key identifiers to X11 keycodes.
func mapKeycode(key, code string) byte {
	// Standard X11 keycodes offset (8 + keysym)
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
		// Fallback for alphanumeric keys
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
