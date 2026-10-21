// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package permission

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

func TestNotGrantedErrorNamesThePermission(t *testing.T) {
	err := fmt.Errorf("screen: capture refused: %w", &NotGrantedError{Permission: ScreenRecording})

	if !errors.Is(err, ErrNotGranted) {
		t.Fatalf("errors.Is(%v, ErrNotGranted) = false", err)
	}
	var ng *NotGrantedError
	if !errors.As(err, &ng) {
		t.Fatalf("errors.As(%v, *NotGrantedError) = false", err)
	}
	if ng.Permission != ScreenRecording {
		t.Fatalf("Permission = %v, want %v", ng.Permission, ScreenRecording)
	}
	if got, want := ng.Error(), "permission: Screen Recording is not granted"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestPermissionNamesAndSettings(t *testing.T) {
	cases := []struct {
		p        Permission
		name     string
		id       string
		settings string
	}{
		{ScreenRecording, "Screen Recording", "screen_recording",
			"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"},
		{Accessibility, "Accessibility", "accessibility",
			"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.name {
			t.Errorf("%d.String() = %q, want %q", c.p, got, c.name)
		}
		if got := c.p.ID(); got != c.id {
			t.Errorf("%v.ID() = %q, want %q", c.p, got, c.id)
		}
		if got := c.p.SettingsURL(); got != c.settings {
			t.Errorf("%v.SettingsURL() = %q, want %q", c.p, got, c.settings)
		}
	}
	if got := Permission(99).String(); got != "Permission(99)" {
		t.Errorf("unknown permission String() = %q", got)
	}
}

func TestStatusString(t *testing.T) {
	for s, want := range map[Status]string{
		Unknown: "unknown", Granted: "granted", NotGranted: "not granted", NotRequired: "not required",
	} {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// Off macOS neither permission exists, so both report NotRequired and asking
// for them does nothing. The darwin answers come from the system and are
// covered by the darwin tests.
func TestOtherPlatformsNeedNoPermission(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS has these permissions")
	}
	for _, p := range []Permission{ScreenRecording, Accessibility} {
		if got := Check(p); got != NotRequired {
			t.Errorf("Check(%v) = %v, want NotRequired", p, got)
		}
		if got := Request(p); got != NotRequired {
			t.Errorf("Request(%v) = %v, want NotRequired", p, got)
		}
	}
}

func TestMissingKeepsThePackagesSentinel(t *testing.T) {
	sentinel := errors.New("input: permission denied (check /dev/uinput permissions)")
	err := Missing(sentinel, Accessibility)

	if !errors.Is(err, sentinel) || !errors.Is(err, ErrNotGranted) {
		t.Fatalf("%v does not match both the sentinel and ErrNotGranted", err)
	}
	var ng *NotGrantedError
	if !errors.As(err, &ng) || ng.Permission != Accessibility {
		t.Fatalf("errors.As found %v", ng)
	}
	// The message is about macOS, not about the sentinel's other platforms.
	if got, want := err.Error(), "permission: Accessibility is not granted"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
