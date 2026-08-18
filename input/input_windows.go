//go:build windows

package input

import (
	"fmt"
	"image"
	"sync"
	"syscall"
	"unsafe"
)

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")

	procSendInput       = modUser32.NewProc("SendInput")
	procGetSystemMetric = modUser32.NewProc("GetSystemMetrics")
)

const (
	inputMouse    uint32 = 0
	inputKeyboard uint32 = 1
	inputHardware uint32 = 2

	mouseEventFMove       uint32 = 0x0001
	mouseEventFLeftDown   uint32 = 0x0002
	mouseEventFLeftUp     uint32 = 0x0004
	mouseEventFRightDown  uint32 = 0x0008
	mouseEventFRightUp    uint32 = 0x0010
	mouseEventFMiddleDown uint32 = 0x0020
	mouseEventFMiddleUp   uint32 = 0x0040
	mouseEventFWheel      uint32 = 0x0800
	mouseEventFHWHL       uint32 = 0x1000
	mouseEventFAbsolute   uint32 = 0x8000

	keyEventFExtendedKey uint32 = 0x0001
	keyEventFKeyUp       uint32 = 0x0002
	keyEventFUnicode     uint32 = 0x0004
	keyEventFScanCode    uint32 = 0x0008

	wheelDelta = 120
)

// Win32 INPUT structure layout (40 bytes on 64-bit Windows)
type winInput struct {
	inputType uint32
	_         uint32 // Padding for 8-byte union alignment on 64-bit
	data      [32]byte
}

type windowsInjector struct {
	mu     sync.Mutex
	bounds image.Rectangle
}

// NewInjector creates a Windows input injector using the Win32 SendInput API.
func NewInjector() (Injector, error) {
	// Initialize default screen bounds using system metrics
	cx, _, _ := procGetSystemMetric.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetric.Call(1) // SM_CYSCREEN

	w := int(cx)
	h := int(cy)
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}

	return &windowsInjector{
		bounds: image.Rect(0, 0, w, h),
	}, nil
}

func (i *windowsInjector) SetScreenBounds(bounds image.Rectangle) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !bounds.Empty() {
		i.bounds = bounds
	}
}

// normalizeCoordinates converts 0.0-1.0 float coordinates to 0-65535 normalized absolute coordinates.
func normalizeCoordinates(x, y float64) (int32, int32) {
	if x < 0 {
		x = 0
	} else if x > 1.0 {
		x = 1.0
	}
	if y < 0 {
		y = 0
	} else if y > 1.0 {
		y = 1.0
	}
	normX := int32(x * 65535.0)
	normY := int32(y * 65535.0)
	return normX, normY
}

func sendInputs(inputs []winInput) error {
	if len(inputs) == 0 {
		return nil
	}
	ret, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])), // #nosec G103 -- required for Win32 SendInput
		uintptr(unsafe.Sizeof(inputs[0])),
	)
	if ret != uintptr(len(inputs)) {
		return fmt.Errorf("SendInput sent %d of %d inputs: %v", ret, len(inputs), err)
	}
	return nil
}

func createMouseInput(dx, dy int32, data uint32, flags uint32) winInput {
	var input winInput
	input.inputType = inputMouse

	// Overlay MOUSEINPUT fields into union data
	// dx (int32, offset 0)
	*(*int32)(unsafe.Pointer(&input.data[0])) = dx // #nosec G103 -- union packing
	// dy (int32, offset 4)
	*(*int32)(unsafe.Pointer(&input.data[4])) = dy // #nosec G103 -- union packing
	// mouseData (uint32, offset 8)
	*(*uint32)(unsafe.Pointer(&input.data[8])) = data // #nosec G103 -- union packing
	// dwFlags (uint32, offset 12)
	*(*uint32)(unsafe.Pointer(&input.data[12])) = flags // #nosec G103 -- union packing

	return input
}

func createKeyboardInput(vk uint16, scan uint16, flags uint32) winInput {
	var input winInput
	input.inputType = inputKeyboard

	// Overlay KEYBDINPUT fields into union data
	// wVk (uint16, offset 0)
	*(*uint16)(unsafe.Pointer(&input.data[0])) = vk // #nosec G103 -- union packing
	// wScan (uint16, offset 2)
	*(*uint16)(unsafe.Pointer(&input.data[2])) = scan // #nosec G103 -- union packing
	// dwFlags (uint32, offset 4)
	*(*uint32)(unsafe.Pointer(&input.data[4])) = flags // #nosec G103 -- union packing

	return input
}

func (i *windowsInjector) MoveMouse(x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	normX, normY := normalizeCoordinates(x, y)
	inp := createMouseInput(normX, normY, 0, mouseEventFAbsolute|mouseEventFMove)
	return sendInputs([]winInput{inp})
}

func (i *windowsInjector) MouseDown(button MouseButton, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	normX, normY := normalizeCoordinates(x, y)
	var flag uint32
	switch button {
	case ButtonLeft:
		flag = mouseEventFLeftDown
	case ButtonMiddle:
		flag = mouseEventFMiddleDown
	case ButtonRight:
		flag = mouseEventFRightDown
	default:
		flag = mouseEventFLeftDown
	}

	inp := createMouseInput(normX, normY, 0, mouseEventFAbsolute|mouseEventFMove|flag)
	return sendInputs([]winInput{inp})
}

func (i *windowsInjector) MouseUp(button MouseButton, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	normX, normY := normalizeCoordinates(x, y)
	var flag uint32
	switch button {
	case ButtonLeft:
		flag = mouseEventFLeftUp
	case ButtonMiddle:
		flag = mouseEventFMiddleUp
	case ButtonRight:
		flag = mouseEventFRightUp
	default:
		flag = mouseEventFLeftUp
	}

	inp := createMouseInput(normX, normY, 0, mouseEventFAbsolute|mouseEventFMove|flag)
	return sendInputs([]winInput{inp})
}

func (i *windowsInjector) Scroll(deltaX, deltaY float64, x, y float64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	normX, normY := normalizeCoordinates(x, y)
	var inputs []winInput

	if deltaY != 0 {
		// Invert deltaY: standard Windows wheel convention (positive = scroll up, negative = scroll down)
		data := uint32(-int32(deltaY * wheelDelta))
		inputs = append(inputs, createMouseInput(normX, normY, data, mouseEventFAbsolute|mouseEventFWheel))
	}

	if deltaX != 0 {
		data := uint32(int32(deltaX * wheelDelta))
		inputs = append(inputs, createMouseInput(normX, normY, data, mouseEventFAbsolute|mouseEventFHWHL))
	}

	return sendInputs(inputs)
}

func (i *windowsInjector) KeyDown(event KeyboardEvent) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	vk, ext := mapKeycode(event.Key, event.Code)
	if vk == 0 {
		if len(event.Key) == 1 {
			// Send as Unicode character
			r := rune(event.Key[0])
			inp := createKeyboardInput(0, uint16(r), keyEventFUnicode)
			return sendInputs([]winInput{inp})
		}
		return nil
	}

	var flags uint32
	if ext {
		flags |= keyEventFExtendedKey
	}

	inp := createKeyboardInput(vk, 0, flags)
	return sendInputs([]winInput{inp})
}

func (i *windowsInjector) KeyUp(event KeyboardEvent) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	vk, ext := mapKeycode(event.Key, event.Code)
	if vk == 0 {
		if len(event.Key) == 1 {
			r := rune(event.Key[0])
			inp := createKeyboardInput(0, uint16(r), keyEventFUnicode|keyEventFKeyUp)
			return sendInputs([]winInput{inp})
		}
		return nil
	}

	flags := keyEventFKeyUp
	if ext {
		flags |= keyEventFExtendedKey
	}

	inp := createKeyboardInput(vk, 0, flags)
	return sendInputs([]winInput{inp})
}

func (i *windowsInjector) Close() error {
	return nil
}

// Windows Virtual-Key codes
const (
	vkBack    uint16 = 0x08
	vkTab     uint16 = 0x09
	vkReturn  uint16 = 0x0D
	vkShift   uint16 = 0x10
	vkControl uint16 = 0x11
	vkMenu    uint16 = 0x12 // Alt
	vkPause   uint16 = 0x13
	vkCapital uint16 = 0x14 // Caps Lock
	vkEscape  uint16 = 0x1B
	vkSpace   uint16 = 0x20
	vkPrior   uint16 = 0x21 // Page Up
	vkNext    uint16 = 0x22 // Page Down
	vkEnd     uint16 = 0x23
	vkHome    uint16 = 0x24
	vkLeft    uint16 = 0x25
	vkUp      uint16 = 0x26
	vkRight   uint16 = 0x27
	vkDown    uint16 = 0x28
	vkSnap    uint16 = 0x2C // PrintScreen
	vkInsert  uint16 = 0x2D
	vkDelete  uint16 = 0x2E
	vkLWin    uint16 = 0x5B
	vkRWin    uint16 = 0x5C
	vkApps    uint16 = 0x5D

	vkNumpad0 uint16 = 0x60
	vkNumpad1 uint16 = 0x61
	vkNumpad2 uint16 = 0x62
	vkNumpad3 uint16 = 0x63
	vkNumpad4 uint16 = 0x64
	vkNumpad5 uint16 = 0x65
	vkNumpad6 uint16 = 0x66
	vkNumpad7 uint16 = 0x67
	vkNumpad8 uint16 = 0x68
	vkNumpad9 uint16 = 0x69

	vkMultiply uint16 = 0x6A
	vkAdd      uint16 = 0x6B
	vkSubtract uint16 = 0x6D
	vkDecimal  uint16 = 0x6E
	vkDivide   uint16 = 0x6F

	vkF1  uint16 = 0x70
	vkF2  uint16 = 0x71
	vkF3  uint16 = 0x72
	vkF4  uint16 = 0x73
	vkF5  uint16 = 0x74
	vkF6  uint16 = 0x75
	vkF7  uint16 = 0x76
	vkF8  uint16 = 0x77
	vkF9  uint16 = 0x78
	vkF10 uint16 = 0x79
	vkF11 uint16 = 0x7A
	vkF12 uint16 = 0x7B

	vkLShift   uint16 = 0xA0
	vkRShift   uint16 = 0xA1
	vkLControl uint16 = 0xA2
	vkRControl uint16 = 0xA3
	vkLMenu    uint16 = 0xA4
	vkRMenu    uint16 = 0xA5

	vkOem1      uint16 = 0xBA // ; :
	vkOemPlus   uint16 = 0xBB // = +
	vkOemComma  uint16 = 0xBC // , <
	vkOemMinus  uint16 = 0xBD // - _
	vkOemPeriod uint16 = 0xBE // . >
	vkOem2      uint16 = 0xBF // / ?
	vkOem3      uint16 = 0xC0 // ` ~
	vkOem4      uint16 = 0xDB // [ {
	vkOem5      uint16 = 0xDC // \ |
	vkOem6      uint16 = 0xDD // ] }
	vkOem7      uint16 = 0xDE // ' "
)

var domCodeToVK = map[string]struct {
	vk  uint16
	ext bool
}{
	// Letters
	"KeyA": {0x41, false}, "KeyB": {0x42, false}, "KeyC": {0x43, false}, "KeyD": {0x44, false},
	"KeyE": {0x45, false}, "KeyF": {0x46, false}, "KeyG": {0x47, false}, "KeyH": {0x48, false},
	"KeyI": {0x49, false}, "KeyJ": {0x4A, false}, "KeyK": {0x4B, false}, "KeyL": {0x4C, false},
	"KeyM": {0x4D, false}, "KeyN": {0x4E, false}, "KeyO": {0x4F, false}, "KeyP": {0x50, false},
	"KeyQ": {0x51, false}, "KeyR": {0x52, false}, "KeyS": {0x53, false}, "KeyT": {0x54, false},
	"KeyU": {0x55, false}, "KeyV": {0x56, false}, "KeyW": {0x57, false}, "KeyX": {0x58, false},
	"KeyY": {0x59, false}, "KeyZ": {0x5A, false},

	// Numbers
	"Digit0": {0x30, false}, "Digit1": {0x31, false}, "Digit2": {0x32, false}, "Digit3": {0x33, false},
	"Digit4": {0x34, false}, "Digit5": {0x35, false}, "Digit6": {0x36, false}, "Digit7": {0x37, false},
	"Digit8": {0x38, false}, "Digit9": {0x39, false},

	// Actions & Controls
	"Enter": {vkReturn, false}, "NumpadEnter": {vkReturn, true}, "Escape": {vkEscape, false},
	"Backspace": {vkBack, false}, "Tab": {vkTab, false}, "Space": {vkSpace, false},
	"CapsLock": {vkCapital, false}, "Capslock": {vkCapital, false},

	// Modifiers
	"ShiftLeft": {vkLShift, false}, "ShiftRight": {vkRShift, true},
	"ControlLeft": {vkLControl, false}, "ControlRight": {vkRControl, true},
	"AltLeft": {vkLMenu, false}, "AltRight": {vkRMenu, true},
	"MetaLeft": {vkLWin, true}, "MetaRight": {vkRWin, true},

	// Navigation & Arrows
	"ArrowUp": {vkUp, true}, "ArrowDown": {vkDown, true}, "ArrowLeft": {vkLeft, true}, "ArrowRight": {vkRight, true},
	"Home": {vkHome, true}, "End": {vkEnd, true}, "PageUp": {vkPrior, true}, "PageDown": {vkNext, true},
	"Insert": {vkInsert, true}, "Delete": {vkDelete, true},

	// Function Keys
	"F1": {vkF1, false}, "F2": {vkF2, false}, "F3": {vkF3, false}, "F4": {vkF4, false},
	"F5": {vkF5, false}, "F6": {vkF6, false}, "F7": {vkF7, false}, "F8": {vkF8, false},
	"F9": {vkF9, false}, "F10": {vkF10, false}, "F11": {vkF11, false}, "F12": {vkF12, false},

	// Symbols
	"Minus": {vkOemMinus, false}, "Equal": {vkOemPlus, false},
	"BracketLeft": {vkOem4, false}, "BracketRight": {vkOem6, false}, "Backslash": {vkOem5, false},
	"Semicolon": {vkOem1, false}, "Quote": {vkOem7, false}, "Backquote": {vkOem3, false},
	"Comma": {vkOemComma, false}, "Period": {vkOemPeriod, false}, "Slash": {vkOem2, false},

	// Numpad
	"Numpad0": {vkNumpad0, false}, "Numpad1": {vkNumpad1, false}, "Numpad2": {vkNumpad2, false},
	"Numpad3": {vkNumpad3, false}, "Numpad4": {vkNumpad4, false}, "Numpad5": {vkNumpad5, false},
	"Numpad6": {vkNumpad6, false}, "Numpad7": {vkNumpad7, false}, "Numpad8": {vkNumpad8, false},
	"Numpad9": {vkNumpad9, false},
	"NumpadMultiply": {vkMultiply, false}, "NumpadAdd": {vkAdd, false},
	"NumpadSubtract": {vkSubtract, false}, "NumpadDecimal": {vkDecimal, false},
	"NumpadDivide": {vkDivide, true},
}

func mapKeycode(key, code string) (uint16, bool) {
	if entry, ok := domCodeToVK[code]; ok && entry.vk > 0 {
		return entry.vk, entry.ext
	}

	switch key {
	case "Enter", "Return":
		return vkReturn, false
	case "Escape", "Esc":
		return vkEscape, false
	case "Backspace":
		return vkBack, false
	case "Tab":
		return vkTab, false
	case "Space", " ":
		return vkSpace, false
	case "Shift":
		return vkShift, false
	case "Control":
		return vkControl, false
	case "Alt":
		return vkMenu, false
	case "Meta", "Super":
		return vkLWin, true
	default:
		if len(key) == 1 {
			r := key[0]
			if r >= 'a' && r <= 'z' {
				return uint16(r - 'a' + 'A'), false
			}
			if r >= 'A' && r <= 'Z' {
				return uint16(r), false
			}
			if r >= '0' && r <= '9' {
				return uint16(r), false
			}
		}
		return 0, false
	}
}
