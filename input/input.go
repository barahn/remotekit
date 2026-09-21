// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package input provides cross-platform remote mouse and keyboard event injection.
package input

import (
	"errors"
	"image"
)

var (
	ErrDeviceNotFound   = errors.New("input: uinput or display server device not found")
	ErrPermissionDenied = errors.New("input: permission denied (check /dev/uinput permissions or root/input group)")
	ErrNotImplemented   = errors.New("input: input injection not implemented on this OS")
)

// MouseButton identifies mouse buttons.
type MouseButton int

const (
	ButtonLeft   MouseButton = 0
	ButtonMiddle MouseButton = 1
	ButtonRight  MouseButton = 2
)

// MouseEvent represents a remote mouse input event.
type MouseEvent struct {
	// X is the normalized horizontal position (0.0 to 1.0) or absolute screen pixel.
	X float64 `json:"x"`

	// Y is the normalized vertical position (0.0 to 1.0) or absolute screen pixel.
	Y float64 `json:"y"`

	// Button is the mouse button involved (0: Left, 1: Middle, 2: Right).
	Button MouseButton `json:"button"`

	// DeltaX is horizontal scroll amount.
	DeltaX float64 `json:"delta_x,omitempty"`

	// DeltaY is vertical scroll amount.
	DeltaY float64 `json:"delta_y,omitempty"`
}

// KeyboardEvent represents a remote keyboard input event.
type KeyboardEvent struct {
	// Key is the string identifier (e.g. "a", "Enter", "Shift").
	Key string `json:"key"`

	// Code is the physical key code (e.g. "KeyA", "Enter").
	Code string `json:"code"`

	// Modifiers
	Ctrl  bool `json:"ctrl"`
	Alt   bool `json:"alt"`
	Shift bool `json:"shift"`
	Meta  bool `json:"meta"`
}

// Injector is the interface satisfied by platform-specific input injectors.
type Injector interface {
	// SetScreenBounds configures target screen dimensions for normalized coordinate scaling.
	SetScreenBounds(bounds image.Rectangle)

	// MoveMouse moves the cursor to the target normalized (0.0 - 1.0) coordinates.
	MoveMouse(x, y float64) error

	// MouseDown depresses a mouse button at the given coordinates.
	MouseDown(button MouseButton, x, y float64) error

	// MouseUp releases a mouse button at the given coordinates.
	MouseUp(button MouseButton, x, y float64) error

	// Scroll injects vertical and horizontal scroll wheel events.
	Scroll(deltaX, deltaY float64, x, y float64) error

	// KeyDown depresses a keyboard key.
	KeyDown(event KeyboardEvent) error

	// KeyUp releases a keyboard key.
	KeyUp(event KeyboardEvent) error

	// Close releases input injection resources.
	Close() error
}
