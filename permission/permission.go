// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package permission reports and requests the operating-system privacy
// permissions that screen capture and input injection depend on.
//
// Only macOS has such permissions today: Screen Recording, which capture
// needs, and Accessibility, which input injection needs. The system grants
// both to the application bundle, and only the person at the machine can grant
// them, in System Settings. This package is mechanism only: it tells a product
// whether a permission is held and can raise the system's own request; how to
// explain that to a person is the product's business.
//
// On every other platform both permissions report NotRequired.
//
// Screen Recording on macOS needs a relaunch after it is granted: Check's
// answer is fixed for the life of the process, and screen capture follows it.
// See Check.
package permission

import (
	"errors"
	"fmt"
)

// Permission is an operating-system privacy permission.
type Permission int

const (
	// ScreenRecording is macOS's Screen Recording permission, which screen
	// capture needs.
	ScreenRecording Permission = iota + 1
	// Accessibility is macOS's Accessibility permission, which input
	// injection needs.
	Accessibility
)

// String returns the permission's name as System Settings shows it.
func (p Permission) String() string {
	switch p {
	case ScreenRecording:
		return "Screen Recording"
	case Accessibility:
		return "Accessibility"
	}
	return fmt.Sprintf("Permission(%d)", int(p))
}

// ID returns a stable identifier for the permission, for wire messages and
// logs: "screen_recording" or "accessibility".
func (p Permission) ID() string {
	switch p {
	case ScreenRecording:
		return "screen_recording"
	case Accessibility:
		return "accessibility"
	}
	return ""
}

// SettingsURL returns the URL that opens the permission's pane in macOS
// System Settings, for a product that wants to take the person there. Opening
// it is up to the product.
func (p Permission) SettingsURL() string {
	const base = "x-apple.systempreferences:com.apple.preference.security?"
	switch p {
	case ScreenRecording:
		return base + "Privacy_ScreenCapture"
	case Accessibility:
		return base + "Privacy_Accessibility"
	}
	return ""
}

// Status is whether a permission is held.
type Status int

const (
	// Unknown means the status could not be read.
	Unknown Status = iota
	// Granted means the permission is held.
	Granted
	// NotGranted means the permission is not held. macOS does not say whether
	// the person refused it or was never asked.
	NotGranted
	// NotRequired means the platform has no such permission.
	NotRequired
)

func (s Status) String() string {
	switch s {
	case Granted:
		return "granted"
	case NotGranted:
		return "not granted"
	case NotRequired:
		return "not required"
	}
	return "unknown"
}

// ErrNotGranted matches, with errors.Is, every error that a missing
// permission caused.
var ErrNotGranted = errors.New("permission: not granted")

// NotGrantedError is the error capture or input returns when the permission
// it needs is not held. Use errors.As to learn which permission is missing.
type NotGrantedError struct {
	Permission Permission
}

func (e *NotGrantedError) Error() string {
	return "permission: " + e.Permission.String() + " is not granted"
}

// Is reports ErrNotGranted as matching.
func (e *NotGrantedError) Is(target error) bool { return target == ErrNotGranted }

// Missing returns the error a package reports when p is not granted. It
// matches sentinel -- the package's own permission error, such as
// screen.ErrPermissionDenied -- as well as ErrNotGranted, and errors.As finds
// the *NotGrantedError in it. Its message is the NotGrantedError's alone, so
// it does not carry advice the sentinel gives for other platforms.
func Missing(sentinel error, p Permission) error {
	return &missingError{sentinel: sentinel, ng: &NotGrantedError{Permission: p}}
}

type missingError struct {
	sentinel error
	ng       *NotGrantedError
}

func (e *missingError) Error() string   { return e.ng.Error() }
func (e *missingError) Unwrap() []error { return []error{e.sentinel, e.ng} }
