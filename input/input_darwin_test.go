// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package input

import (
	"errors"
	"image"
	"math"
	"testing"
	"time"

	"github.com/barahn/remotekit/internal/macos"
	"github.com/barahn/remotekit/permission"
)

// These post real events. Which one runs depends on whether the test binary
// holds Accessibility: run from a terminal it normally does not, and the
// refusal is checked; run from an app bundle that was granted it, the pointer
// is moved and read back.

func pointer() macos.CGPoint {
	ev := macos.CGEventCreate(0)
	defer macos.CFRelease(ev)
	return macos.CGEventGetLocation(ev)
}

func TestDarwinInjectorWithoutAccessibility(t *testing.T) {
	if st := permission.Check(permission.Accessibility); st != permission.NotGranted {
		t.Skipf("Accessibility is %v here", st)
	}
	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	err = inj.MoveMouse(0.5, 0.5)
	var ng *permission.NotGrantedError
	if !errors.As(err, &ng) || ng.Permission != permission.Accessibility {
		t.Fatalf("MoveMouse() = %v, want the Accessibility error", err)
	}
	t.Logf("refused as expected: %v", err)
}

func TestDarwinInjectorMovesThePointer(t *testing.T) {
	if st := permission.Check(permission.Accessibility); st != permission.Granted {
		t.Skipf("Accessibility is %v here", st)
	}
	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	main := cgPoster{}.mainBounds()
	inj.SetScreenBounds(main)
	for _, f := range [][2]float64{{0.25, 0.25}, {0.75, 0.5}, {0, 0}} {
		if err := inj.MoveMouse(f[0], f[1]); err != nil {
			t.Fatalf("MoveMouse: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		want := image.Pt(main.Min.X+int(f[0]*float64(main.Dx()-1)), main.Min.Y+int(f[1]*float64(main.Dy()-1)))
		got := pointer()
		if math.Abs(got.X-float64(want.X)) > 1 || math.Abs(got.Y-float64(want.Y)) > 1 {
			t.Errorf("MoveMouse(%v, %v): pointer at (%v, %v), want %v", f[0], f[1], got.X, got.Y, want)
		}
	}
	// These must post without error. What they do on screen is checked by
	// hand: typing here would land in whatever has focus.
	if err := inj.Scroll(0, 1, 0, 0); err != nil {
		t.Errorf("Scroll: %v", err)
	}
	shift := KeyboardEvent{Key: "Shift", Code: "ShiftLeft"}
	if err := inj.KeyDown(shift); err != nil {
		t.Errorf("KeyDown(Shift): %v", err)
	}
	if err := inj.KeyUp(shift); err != nil {
		t.Errorf("KeyUp(Shift): %v", err)
	}
}
