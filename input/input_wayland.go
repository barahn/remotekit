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
	mutterRemoteDesktopDest = "org.gnome.Mutter.RemoteDesktop"
	mutterRemoteDesktopPath = "/org/gnome/Mutter/RemoteDesktop"
	mutterRemoteDesktop     = "org.gnome.Mutter.RemoteDesktop"
	mutterRemoteSession     = "org.gnome.Mutter.RemoteDesktop.Session"

	portalDest          = "org.freedesktop.portal.Desktop"
	portalPath          = "/org/freedesktop/portal/desktop"
	portalRemoteDesktop = "org.freedesktop.portal.RemoteDesktop"

	// Linux evdev mouse button codes
	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
)

// waylandInjector implements Injector on Wayland via GNOME Mutter RemoteDesktop and XDG Desktop Portal.
type waylandInjector struct {
	mu          sync.Mutex
	bus         *dbus.Conn
	session     dbus.ObjectPath
	isMutter    bool
	bounds      image.Rectangle
	x11Fallback Injector
}

func newWaylandInjector() (*waylandInjector, error) {
	bus, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("wayland input: cannot connect to D-Bus session bus: %w", err)
	}

	var x11Fallback Injector
	if linj, err := newX11Injector(); err == nil && linj != nil {
		x11Fallback = linj
	}

	inj := &waylandInjector{
		bus:         bus,
		bounds:      image.Rect(0, 0, 1920, 1080),
		x11Fallback: x11Fallback,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 1. Try GNOME Mutter native RemoteDesktop first (Mutter D-Bus Method)
	mutterObj := bus.Object(mutterRemoteDesktopDest, dbus.ObjectPath(mutterRemoteDesktopPath))
	var mutterSession dbus.ObjectPath
	if err := mutterObj.CallWithContext(ctx, mutterRemoteDesktop+".CreateSession", 0).Store(&mutterSession); err == nil {
		inj.session = mutterSession
		inj.isMutter = true
		sessObj := bus.Object(mutterRemoteDesktopDest, mutterSession)
		_ = sessObj.CallWithContext(ctx, mutterRemoteSession+".SelectDevices", 0, uint32(7)).Store() // Pointer + Keyboard + Touch
		_ = sessObj.CallWithContext(ctx, mutterRemoteSession+".Start", 0).Store()
		return inj, nil
	}

	// 2. Try XDG Desktop Portal RemoteDesktop
	obj := bus.Object(portalDest, dbus.ObjectPath(portalPath))

	sessionToken := fmt.Sprintf("barahn_input_%d", time.Now().UnixNano())
	createOptions := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(sessionToken),
	}

	var sessionHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalRemoteDesktop+".CreateSession", 0, createOptions).Store(&sessionHandle)
	if err != nil {
		if x11Fallback != nil {
			return inj, nil
		}
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

func (inj *waylandInjector) getX11Fallback() Injector {
	if inj.x11Fallback != nil {
		return inj.x11Fallback
	}
	if linj, err := newX11Injector(); err == nil && linj != nil {
		inj.x11Fallback = linj
		linj.SetScreenBounds(inj.bounds)
		return linj
	}
	return nil
}

func (inj *waylandInjector) MoveMouse(x, y float64) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.bus == nil || inj.session == "" {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.MoveMouse(x, y)
		}
		return nil
	}

	px := x * float64(inj.bounds.Dx())
	py := y * float64(inj.bounds.Dy())

	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		err := sessObj.Call(mutterRemoteSession+".NotifyPointerMotionAbsolute", 0, "", px, py).Err
		if err != nil {
			err = sessObj.Call(mutterRemoteSession+".NotifyPointerMotionRelative", 0, 0.0, 0.0).Err
		}
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.MoveMouse(x, y)
			}
			return nil
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	err := obj.Call(portalRemoteDesktop+".NotifyPointerMotionAbsolute", 0, inj.session, options, uint32(0), px, py).Err
	if err != nil {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.MoveMouse(x, y)
		}
		return nil
	}
	return nil
}

func (inj *waylandInjector) MouseDown(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	btnCode := mapWaylandButton(button)
	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		err := sessObj.Call(mutterRemoteSession+".NotifyPointerButton", 0, int32(btnCode), true).Err
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.MouseDown(button, x, y)
			}
			return nil
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	err := obj.Call(portalRemoteDesktop+".NotifyPointerButton", 0, inj.session, options, int32(btnCode), uint32(1)).Err
	if err != nil {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.MouseDown(button, x, y)
		}
		return nil
	}
	return nil
}

func (inj *waylandInjector) MouseUp(button MouseButton, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	btnCode := mapWaylandButton(button)
	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		err := sessObj.Call(mutterRemoteSession+".NotifyPointerButton", 0, int32(btnCode), false).Err
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.MouseUp(button, x, y)
			}
			return nil
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	err := obj.Call(portalRemoteDesktop+".NotifyPointerButton", 0, inj.session, options, int32(btnCode), uint32(0)).Err
	if err != nil {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.MouseUp(button, x, y)
		}
		return nil
	}
	return nil
}

func (inj *waylandInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	if err := inj.MoveMouse(x, y); err != nil {
		return err
	}

	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		if deltaY != 0 {
			err := sessObj.Call(mutterRemoteSession+".NotifyPointerAxisDiscrete", 0, uint32(0), int32(deltaY)).Err
			if err != nil {
				_ = sessObj.Call(mutterRemoteSession+".NotifyPointerAxis", 0, float64(deltaX), float64(deltaY), uint32(0)).Err
			}
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{
		"axis": dbus.MakeVariant(uint32(0)),
	}

	if deltaY != 0 {
		options["finish"] = dbus.MakeVariant(false)
		err := obj.Call(portalRemoteDesktop+".NotifyPointerAxis", 0, inj.session, options, uint32(0), float64(deltaX), float64(deltaY)).Err
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.Scroll(deltaX, deltaY, x, y)
			}
			return nil
		}
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

	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		err := sessObj.Call(mutterRemoteSession+".NotifyKeyboardKeycode", 0, evdevCode, true).Err
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.KeyDown(event)
			}
			return nil
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	err := obj.Call(portalRemoteDesktop+".NotifyKeyboardKeycode", 0, inj.session, options, int32(evdevCode), uint32(1)).Err // #nosec G115 -- evdevCode fits in int32
	if err != nil {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.KeyDown(event)
		}
		return nil
	}
	return nil
}

func (inj *waylandInjector) KeyUp(event KeyboardEvent) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	evdevCode := mapToEvdevKeycode(event.Key, event.Code)
	if evdevCode == 0 {
		return nil
	}

	if inj.isMutter {
		sessObj := inj.bus.Object(mutterRemoteDesktopDest, inj.session)
		err := sessObj.Call(mutterRemoteSession+".NotifyKeyboardKeycode", 0, evdevCode, false).Err
		if err != nil {
			if fb := inj.getX11Fallback(); fb != nil {
				return fb.KeyUp(event)
			}
			return nil
		}
		return nil
	}

	obj := inj.bus.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{}
	err := obj.Call(portalRemoteDesktop+".NotifyKeyboardKeycode", 0, inj.session, options, int32(evdevCode), uint32(0)).Err // #nosec G115 -- evdevCode fits in int32
	if err != nil {
		if fb := inj.getX11Fallback(); fb != nil {
			return fb.KeyUp(event)
		}
		return nil
	}
	return nil
}

func (inj *waylandInjector) Close() error {
	inj.mu.Lock()
	defer inj.mu.Unlock()

	if inj.x11Fallback != nil {
		_ = inj.x11Fallback.Close()
	}

	if inj.session != "" && inj.bus != nil {
		if inj.isMutter {
			_ = inj.bus.Object(mutterRemoteDesktopDest, inj.session).Call(mutterRemoteSession+".Stop", 0).Store()
		} else {
			_ = inj.bus.Object(portalDest, inj.session).Call("org.freedesktop.portal.Session.Close", 0).Store()
		}
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
func mapToEvdevKeycode(key, code string) uint32 {
	x11Code := mapKeycode(key, code)
	if x11Code >= 8 {
		// Standard Linux evdev keycode = X11 keycode - 8
		return uint32(x11Code - 8)
	}
	return 0
}
