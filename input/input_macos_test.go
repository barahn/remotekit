// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package input

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/permission"
)

// fakeMacPoster records what the macOS injector would post, so its logic runs
// on every platform's CI.
type fakeMacPoster struct {
	isTrusted bool
	checks    int
	events    []string
}

func (p *fakeMacPoster) trusted() bool { p.checks++; return p.isTrusted }

func (p *fakeMacPoster) mainBounds() image.Rectangle { return image.Rect(0, 0, 1440, 900) }

func (p *fakeMacPoster) mouse(typ uint32, x, y float64, button uint32, clicks int64, flags uint64) error {
	p.events = append(p.events, fmt.Sprintf("mouse %s (%g,%g) button=%d clicks=%d mods:%s", macMouseTypeName(typ), x, y, button, clicks, flagNames(flags)))
	return nil
}

func (p *fakeMacPoster) key(code uint16, down bool, flags uint64) error {
	p.events = append(p.events, fmt.Sprintf("vk %d %s mods:%s", code, upDown(down), flagNames(flags)))
	return nil
}

func (p *fakeMacPoster) flagsChanged(code uint16, flags uint64) error {
	p.events = append(p.events, fmt.Sprintf("flags %d mods:%s", code, flagNames(flags)))
	return nil
}

func (p *fakeMacPoster) text(s string, down bool) error {
	p.events = append(p.events, fmt.Sprintf("text %q %s", s, upDown(down)))
	return nil
}

func (p *fakeMacPoster) scroll(vertical, horizontal int32) error {
	p.events = append(p.events, fmt.Sprintf("scroll v=%d h=%d", vertical, horizontal))
	return nil
}

func (p *fakeMacPoster) take() []string {
	e := p.events
	p.events = nil
	return e
}

func upDown(down bool) string {
	if down {
		return "down"
	}
	return "up"
}

func flagNames(f uint64) string {
	var n []string
	for _, m := range []struct {
		bit  uint64
		name string
	}{{cgFlagShift, "shift"}, {cgFlagControl, "ctrl"}, {cgFlagOption, "opt"}, {cgFlagCommand, "cmd"},
		{cgFlagNumericPad, "numpad"}, {cgFlagSecondaryFn, "fn"}} {
		if f&m.bit != 0 {
			n = append(n, m.name)
			f &^= m.bit
		}
	}
	if f != 0 {
		n = append(n, fmt.Sprintf("%#x", f))
	}
	if len(n) == 0 {
		return "-"
	}
	return strings.Join(n, "+")
}

func macMouseTypeName(t uint32) string {
	names := map[uint32]string{
		cgLeftMouseDown: "leftDown", cgLeftMouseUp: "leftUp", cgRightMouseDown: "rightDown", cgRightMouseUp: "rightUp",
		cgMouseMoved: "moved", cgLeftMouseDragged: "leftDragged", cgRightMouseDragged: "rightDragged",
		cgOtherMouseDown: "otherDown", cgOtherMouseUp: "otherUp", cgOtherMouseDragged: "otherDragged",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprint(t)
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock           { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func newTestMacInjector() (*macInjector, *fakeMacPoster, *fakeClock) {
	p := &fakeMacPoster{isTrusted: true}
	clock := newFakeClock()
	return newMacInjector(p, clock.now), p, clock
}

func expectEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("posted:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Without Accessibility macOS drops posted events without a word, so the
// injector must say why nothing happens, naming the permission.
func TestMacInjectorWithoutAccessibility(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	p.isTrusted = false

	for name, call := range map[string]func() error{
		"MoveMouse": func() error { return inj.MoveMouse(0.5, 0.5) },
		"MouseDown": func() error { return inj.MouseDown(ButtonLeft, 0.5, 0.5) },
		"MouseUp":   func() error { return inj.MouseUp(ButtonLeft, 0.5, 0.5) },
		"Scroll":    func() error { return inj.Scroll(0, 1, 0.5, 0.5) },
		"KeyDown":   func() error { return inj.KeyDown(KeyboardEvent{Key: "a", Code: "KeyA"}) },
		"KeyUp":     func() error { return inj.KeyUp(KeyboardEvent{Key: "a", Code: "KeyA"}) },
	} {
		err := call()
		var ng *permission.NotGrantedError
		if !errors.As(err, &ng) || ng.Permission != permission.Accessibility {
			t.Errorf("%s() = %v, want the Accessibility error", name, err)
		}
		if !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("%s() = %v, want it to match ErrPermissionDenied", name, err)
		}
		if errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s(): a missing permission reads as a missing backend", name)
		}
	}
	expectEvents(t, p.take())
}

// The permission is read at most once a second, not once per mouse move, and
// a grant made mid-session is picked up without a restart.
func TestMacInjectorRechecksAccessibility(t *testing.T) {
	inj, p, clock := newTestMacInjector()
	p.isTrusted = false
	_ = inj.MoveMouse(0.1, 0.1)
	_ = inj.MoveMouse(0.2, 0.2)
	if p.checks != 1 {
		t.Fatalf("checked %d times within a second, want 1", p.checks)
	}
	p.isTrusted = true
	clock.add(1100 * time.Millisecond)
	if err := inj.MoveMouse(0.3, 0.3); err != nil {
		t.Fatalf("MoveMouse after the grant = %v", err)
	}
}

// Normalized coordinates land on the monitor being streamed, in points, at
// its global offset -- a monitor left of the main one has a negative origin.
func TestMacInjectorAimsAtTheStreamedMonitor(t *testing.T) {
	inj, p, _ := newTestMacInjector()

	_ = inj.MoveMouse(0.5, 0.5) // before any bounds: the main display
	inj.SetScreenBounds(image.Rect(-1920, 0, 0, 1080))
	_ = inj.MoveMouse(0, 0)
	_ = inj.MoveMouse(1, 1)
	_ = inj.MoveMouse(-3, 7) // clamped
	inj.SetScreenBounds(image.Rectangle{})
	_ = inj.MoveMouse(0, 0) // an empty rectangle is ignored

	expectEvents(t, p.take(),
		"mouse moved (719.5,449.5) button=0 clicks=0 mods:-",
		"mouse moved (-1920,0) button=0 clicks=0 mods:-",
		"mouse moved (-1,1079) button=0 clicks=0 mods:-",
		"mouse moved (-1920,1079) button=0 clicks=0 mods:-",
		"mouse moved (-1920,0) button=0 clicks=0 mods:-",
	)
}

func TestMacInjectorButtonsAndDrag(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	inj.SetScreenBounds(image.Rect(0, 0, 101, 101))

	_ = inj.MouseDown(ButtonLeft, 0.1, 0.1)
	_ = inj.MoveMouse(0.2, 0.2) // a move with the button held is a drag
	_ = inj.MouseUp(ButtonLeft, 0.2, 0.2)
	_ = inj.MoveMouse(0.3, 0.3)
	_ = inj.MouseDown(ButtonRight, 0.3, 0.3)
	_ = inj.MoveMouse(0.4, 0.4)
	_ = inj.MouseUp(ButtonRight, 0.4, 0.4)
	_ = inj.MouseDown(ButtonMiddle, 0.4, 0.4)
	_ = inj.MoveMouse(0.5, 0.5)
	_ = inj.MouseUp(ButtonMiddle, 0.5, 0.5)

	expectEvents(t, p.take(),
		"mouse leftDown (10,10) button=0 clicks=1 mods:-",
		"mouse leftDragged (20,20) button=0 clicks=0 mods:-",
		"mouse leftUp (20,20) button=0 clicks=1 mods:-",
		"mouse moved (30,30) button=0 clicks=0 mods:-",
		"mouse rightDown (30,30) button=1 clicks=1 mods:-",
		"mouse rightDragged (40,40) button=1 clicks=0 mods:-",
		"mouse rightUp (40,40) button=1 clicks=1 mods:-",
		"mouse otherDown (40,40) button=2 clicks=1 mods:-",
		"mouse otherDragged (50,50) button=2 clicks=0 mods:-",
		"mouse otherUp (50,50) button=2 clicks=1 mods:-",
	)
}

// macOS recognises a double click only from the click count on the event, so
// the injector has to count: two presses close in time and place are a double
// click, a third a triple.
func TestMacInjectorCountsClicks(t *testing.T) {
	inj, p, clock := newTestMacInjector()
	inj.SetScreenBounds(image.Rect(0, 0, 1001, 1001))

	click := func(x float64) {
		_ = inj.MouseDown(ButtonLeft, x, 0.5)
		_ = inj.MouseUp(ButtonLeft, x, 0.5)
	}
	click(0.5)
	clock.add(200 * time.Millisecond)
	click(0.5)
	clock.add(200 * time.Millisecond)
	click(0.501) // a point away still counts
	clock.add(600 * time.Millisecond)
	click(0.501) // too late: a new single click
	clock.add(100 * time.Millisecond)
	click(0.6) // too far: a new single click
	_ = inj.MouseDown(ButtonRight, 0.6, 0.5)

	var counts []string
	for _, e := range p.take() {
		counts = append(counts, e[strings.Index(e, "clicks="):strings.Index(e, " mods")])
	}
	want := "clicks=1 clicks=1 clicks=2 clicks=2 clicks=3 clicks=3 clicks=1 clicks=1 clicks=1 clicks=1 clicks=1"
	if got := strings.Join(counts, " "); got != want {
		t.Errorf("click counts %s\nwant         %s", got, want)
	}
}

// One wheel event scrolls three lines, in the direction of the delta's sign,
// as a wheel notch does elsewhere. A positive delta scrolls down or right,
// which on macOS is a negative wheel value.
func TestMacInjectorScroll(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	inj.SetScreenBounds(image.Rect(0, 0, 101, 101))

	_ = inj.Scroll(0, 120, 0.5, 0.5)
	_ = inj.Scroll(0, -1, 0.5, 0.5)
	_ = inj.Scroll(2, 0, 0.5, 0.5)
	_ = inj.Scroll(-0.5, 3, 0.5, 0.5)
	_ = inj.Scroll(0, 0, 0.5, 0.5)

	expectEvents(t, p.take(),
		"mouse moved (50,50) button=0 clicks=0 mods:-", "scroll v=-3 h=0",
		"mouse moved (50,50) button=0 clicks=0 mods:-", "scroll v=3 h=0",
		"mouse moved (50,50) button=0 clicks=0 mods:-", "scroll v=0 h=-3",
		"mouse moved (50,50) button=0 clicks=0 mods:-", "scroll v=-3 h=3",
	)
}

// Typed characters go in as text, whatever the Mac's keyboard layout, so a
// viewer on an ABNT2 or AZERTY keyboard types what it shows -- accents too.
func TestMacInjectorTypesText(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	for _, ev := range []KeyboardEvent{
		{Key: "a", Code: "KeyA"},
		{Key: "A", Code: "KeyA", Shift: true},
		{Key: "é", Code: "KeyE"},
		{Key: "Ç", Code: "Semicolon", Shift: true},
		{Key: "~", Code: "Quote", Shift: true},
		{Key: "€", Code: "KeyE", Ctrl: true, Alt: true}, // AltGr on a Windows viewer
		{Key: "😀"},
	} {
		_ = inj.KeyDown(ev)
		_ = inj.KeyUp(ev)
	}
	expectEvents(t, p.take(),
		`text "a" down`, `text "a" up`,
		`text "A" down`, `text "A" up`,
		`text "é" down`, `text "é" up`,
		`text "Ç" down`, `text "Ç" up`,
		`text "~" down`, `text "~" up`,
		`text "€" down`, `text "€" up`,
		`text "😀" down`, `text "😀" up`,
	)
}

// A dead key composes nothing by itself: the viewer's next key carries the
// composed character, so posting the dead key's position as well would type
// the wrong character on the Mac.
func TestMacInjectorIgnoresDeadKeys(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	_ = inj.KeyDown(KeyboardEvent{Key: "Dead", Code: "BracketLeft"})
	_ = inj.KeyUp(KeyboardEvent{Key: "Dead", Code: "BracketLeft"})
	_ = inj.KeyDown(KeyboardEvent{Key: "Process", Code: "KeyA"})
	expectEvents(t, p.take())
}

// Ctrl is Control and Meta is Command: the injector types what the viewer
// pressed. A product that wants a Windows technician's Ctrl+C to copy on a Mac
// maps it in the viewer.
func TestMacInjectorShortcutsUseKeyPositions(t *testing.T) {
	inj, p, _ := newTestMacInjector()

	meta := KeyboardEvent{Key: "Meta", Code: "MetaLeft", Meta: true}
	cmdC := KeyboardEvent{Key: "c", Code: "KeyC", Meta: true}
	_ = inj.KeyDown(meta)
	_ = inj.KeyDown(cmdC)
	_ = inj.KeyUp(cmdC)
	_ = inj.KeyUp(KeyboardEvent{Key: "Meta", Code: "MetaLeft"})

	ctrlA := KeyboardEvent{Key: "a", Code: "KeyA", Ctrl: true}
	_ = inj.KeyDown(ctrlA)
	_ = inj.KeyUp(ctrlA)

	// A viewer that reports the modifier keys but not the flags.
	_ = inj.KeyDown(KeyboardEvent{Key: "Alt", Code: "AltLeft"})
	_ = inj.KeyDown(KeyboardEvent{Key: "Shift", Code: "ShiftRight"})
	_ = inj.KeyDown(KeyboardEvent{Key: "z", Code: "KeyZ"})
	_ = inj.KeyUp(KeyboardEvent{Key: "Shift", Code: "ShiftRight"})
	_ = inj.KeyUp(KeyboardEvent{Key: "Alt", Code: "AltLeft"})

	expectEvents(t, p.take(),
		"flags 55 mods:cmd",
		"vk 8 down mods:cmd",
		"vk 8 up mods:cmd",
		"flags 55 mods:-",
		"vk 0 down mods:ctrl",
		"vk 0 up mods:ctrl",
		"flags 58 mods:opt",
		"flags 60 mods:shift+opt",
		"vk 6 down mods:shift+opt",
		"flags 60 mods:opt",
		"flags 58 mods:-",
	)
}

func TestMacInjectorModifiersApplyToClicks(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	inj.SetScreenBounds(image.Rect(0, 0, 101, 101))
	_ = inj.KeyDown(KeyboardEvent{Key: "Meta", Code: "MetaLeft", Meta: true})
	_ = inj.MouseDown(ButtonLeft, 0.5, 0.5)
	expectEvents(t, p.take(),
		"flags 55 mods:cmd",
		"mouse leftDown (50,50) button=0 clicks=1 mods:cmd",
	)
}

func TestMacInjectorNamedKeys(t *testing.T) {
	inj, p, _ := newTestMacInjector()
	for _, ev := range []KeyboardEvent{
		{Key: "Enter", Code: "Enter"},
		{Key: "Backspace", Code: "Backspace"},
		{Key: "Delete", Code: "Delete"},
		{Key: "ArrowLeft", Code: "ArrowLeft", Shift: true},
		{Key: "F5", Code: "F5"},
		{Key: " ", Code: "Space"},
		{Key: "Tab"},    // no code: by name
		{Key: "Escape"}, // no code: by name
		{Key: "v", Ctrl: true},
		{Key: "Unidentified", Code: "NoSuchKey"},
	} {
		_ = inj.KeyDown(ev)
	}
	expectEvents(t, p.take(),
		"vk 36 down mods:-",
		"vk 51 down mods:-",
		"vk 117 down mods:fn",
		"vk 123 down mods:shift+numpad+fn",
		"vk 96 down mods:fn",
		"vk 49 down mods:-",
		"vk 48 down mods:-",
		"vk 53 down mods:-",
		"vk 9 down mods:ctrl",
	)
}

func TestMacKeycodesCoverTheOtherBackends(t *testing.T) {
	// Every DOM code the Windows backend maps, macOS maps too (Windows'
	// table is the most complete of the others).
	codes := []string{
		"KeyA", "KeyZ", "Digit0", "Digit9", "Enter", "NumpadEnter", "Escape", "Backspace", "Tab", "Space", "CapsLock",
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight", "MetaLeft", "MetaRight",
		"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Insert", "Delete",
		"F1", "F12", "Minus", "Equal", "BracketLeft", "BracketRight", "Backslash", "Semicolon", "Quote", "Backquote",
		"Comma", "Period", "Slash", "Numpad0", "Numpad9", "NumpadMultiply", "NumpadAdd", "NumpadSubtract",
		"NumpadDecimal", "NumpadDivide",
	}
	for _, c := range codes {
		_, isKey := macKeyByCode[c]
		_, _, isModifier := macModifier(KeyboardEvent{Code: c})
		if !isKey && !isModifier {
			t.Errorf("no macOS key code for %q", c)
		}
	}
}
