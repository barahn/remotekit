//go:build linux

package input

import (
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest          = "org.freedesktop.portal.Desktop"
	portalPath          = "/org/freedesktop/portal/desktop"
	portalRemoteDesktop = "org.freedesktop.portal.RemoteDesktop"

	// Linux evdev mouse button codes
	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
)

// waylandInjector implements Injector on Wayland via XDG Desktop Portal RemoteDesktop interface.
type waylandInjector struct {
	mu      sync.Mutex
	bus     *dbus.Conn
	session dbus.ObjectPath
	bounds  image.Rectangle
}

func newWaylandInjector() (*waylandInjector, error) {
	bus, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("wayland input: cannot connect to D-Bus session bus: %w", err)
	}

	inj := &waylandInjector{
		bus: bus,
		bounds: image.Rect(0, 0, 1920, 1080),
	}

	// Try to initialize portal RemoteDesktop session
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	obj := bus.Object(portalDest, dbus.ObjectPath(portalPath))

	sessionToken := fmt.Sprintf("barahn_input_%d", time.Now().UnixNano())
	createOptions := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(sessionToken),
	}

	var sessionHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalRemoteDesktop+".CreateSession", 0, createOptions).Store(&sessionHandle)
	if err != nil {
		return nil, fmt.Errorf("RemoteDesktop CreateSession failed: %w", err)
	}
	inj.session = sessionHandle

	selectToken := fmt.Sprintf("barahn_devices_%d", time.Now().UnixNano())
	selectOptions := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(7)), // 1=Keyboard, 2=Pointer, 4=Touch
		"handle_token": dbus.MakeVariant(selectToken),
	}

	var selectHandle dbus.ObjectPath
	_ = obj.CallWithContext(ctx, portalRemoteDesktop+".SelectDevices", 0, inj.session, selectOptions).Store(&selectHandle)

	startToken := fmt.Sprintf("barahn_input_start_%d", time.Now().UnixNano())
	startOptions := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(startToken),
	}

	var startHandle dbus.ObjectPath
	_ = obj.CallWithContext(ctx, portalRemoteDesktop+".Start", 0, inj.session, "", startOptions).Store(&startHandle)

	return inj, nil
}

func (inj *waylandInjector) SetScreenBounds(bounds image.Rectangle) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	if !bounds.Empty() {
		inj.bounds = bounds
	}
}

func (inj *waylandInjector) MoveMouse(x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.bus == nil || inj.session == "" {
		return ErrDeviceNotFound
	}

	px := x * float64(inj.bounds.Dx())
	py := y * float64(inj.bounds.Dy())

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	return obj.Call(portalRemoteDesktop+".NotifyPointerMotionAbsolute", 0, inj.session, options, uint32(0), px, py).Err
}

func (inj *waylandInjector) MouseDown(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	btnCode := mapWaylandButton(button)
	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	return obj.Call(portalRemoteDesktop+".NotifyPointerButton", 0, inj.session, options, int32(btnCode), uint32(1)).Err
}

func (inj *waylandInjector) MouseUp(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	btnCode := mapWaylandButton(button)
	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	return obj.Call(portalRemoteDesktop+".NotifyPointerButton", 0, inj.session, options, int32(btnCode), uint32(0)).Err
}

func (inj *waylandInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{
		"axis": dbus.MakeVariant(uint32(0)),
	}

	if deltaY != 0 {
		options["finish"] = dbus.MakeVariant(false)
		_ = obj.Call(portalRemoteDesktop+".NotifyPointerAxis", 0, inj.session, options, uint32(0), float64(deltaX), float64(deltaY)).Err
	}
	return nil
}

func (inj *waylandInjector) KeyDown(event KeyboardEvent) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	evdevCode := mapToEvdevKeycode(event.Key, event.Code)
	if evdevCode == 0 {
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	return obj.Call(portalRemoteDesktop+".NotifyKeyboardKeycode", 0, inj.session, options, int32(evdevCode), uint32(1)).Err
}

func (inj *waylandInjector) KeyUp(event KeyboardEvent) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	evdevCode := mapToEvdevKeycode(event.Key, event.Code)
	if evdevCode == 0 {
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	return obj.Call(portalRemoteDesktop+".NotifyKeyboardKeycode", 0, inj.session, options, int32(evdevCode), uint32(0)).Err
}

func (inj *waylandInjector) Close() error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.session != "" && inj.bus != nil {
		_ = inj.bus.Object(portalDest, inj.session).Call("org.freedesktop.portal.Session.Close", 0).Store()
		inj.session = ""
	}
	return nil
}

func mapWaylandButton(button MouseButton) int32 {
	switch button {
	case ButtonLeft:
		return btnLeft
	case ButtonMiddle:
		return btnMiddle
	case ButtonRight:
		return btnRight
	default:
		return btnLeft
	}
}

// mapToEvdevKeycode maps DOM codes/keys to Linux kernel evdev keycodes (KEY_* from linux/input-event-codes.h)
func mapToEvdevKeycode(key, code string) int32 {
	x11Code := mapKeycode(key, code)
	if x11Code >= 8 {
		// Standard Linux evdev keycode = X11 keycode - 8
		return int32(x11Code - 8)
	}
	return 0
}
