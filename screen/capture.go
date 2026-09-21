// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package screen provides platform-agnostic screen capture capabilities.
//
// The Capturer interface abstracts platform-specific screen capture
// implementations (X11, Wayland, DXGI, CGDisplay) behind a uniform API.
// Captured frames are delivered via a channel for non-blocking consumption
// by encoding and transport layers.
package screen

import (
	"context"
	"errors"
	"image"
	"time"
)

// Sentinel errors for screen capture operations.
var (
	ErrDisplayNotFound  = errors.New("screen: display not found")
	ErrCaptureNotReady  = errors.New("screen: capture not initialized")
	ErrAlreadyStarted   = errors.New("screen: capture already started")
	ErrPermissionDenied = errors.New("screen: permission denied (check display server access)")
)

// Frame represents a single captured screen frame.
type Frame struct {
	// Image holds the raw RGBA pixel data of the captured frame.
	Image *image.RGBA

	// Bounds is the rectangle of the captured display area.
	Bounds image.Rectangle

	// DisplayIndex identifies which display this frame was captured from.
	DisplayIndex int

	// CapturedAt is the timestamp when the frame was captured.
	CapturedAt time.Time

	// SequenceNum is a monotonically increasing frame counter.
	SequenceNum uint64
}

// Display represents a physical display/monitor attached to the system.
type Display struct {
	// Index is the zero-based display identifier.
	Index int

	// Name is a human-readable display name (e.g., "HDMI-1", "eDP-1").
	Name string

	// Bounds is the display geometry in global screen coordinates.
	Bounds image.Rectangle

	// Primary indicates whether this is the primary/default display.
	Primary bool
}

// CaptureConfig controls capture behavior.
type CaptureConfig struct {
	// TargetFPS is the desired capture frame rate. Zero means maximum rate.
	TargetFPS int

	// DisplayIndex selects which display to capture. Use -1 for primary.
	DisplayIndex int

	// IncludeCursor controls whether the mouse cursor is rendered into frames.
	IncludeCursor bool

	// FrameBufferSize controls how many frames can be buffered before
	// the oldest frame is dropped. This prevents slow consumers from
	// causing memory buildup. Zero defaults to 2.
	FrameBufferSize int
}

// DefaultConfig returns a CaptureConfig with sensible defaults.
func DefaultConfig() CaptureConfig {
	return CaptureConfig{
		TargetFPS:       30,
		DisplayIndex:    -1,
		IncludeCursor:   true,
		FrameBufferSize: 2,
	}
}

// Capturer is the platform-agnostic interface for screen capture.
//
// Implementations must be safe for concurrent use: Start, Stop, and
// Frames may be called from different goroutines. The Frames channel
// is closed when Stop is called or the context is cancelled.
type Capturer interface {
	// Displays enumerates available displays/monitors.
	// Returns an empty slice if no displays are detected.
	Displays() ([]Display, error)

	// Start begins capturing frames from the configured display.
	// Frames are delivered on the channel returned by Frames().
	// Returns ErrAlreadyStarted if capture is already in progress.
	Start(ctx context.Context) error

	// Frames returns a read-only channel that delivers captured frames.
	// The channel is created during Start and closed during Stop.
	// Consumers should range over this channel.
	// Frames may be dropped if the consumer is slower than the producer
	// (the buffer size is controlled by CaptureConfig.FrameBufferSize).
	Frames() <-chan *Frame

	// Stop halts capture and closes the Frames channel.
	// It is safe to call Stop multiple times.
	Stop()

	// SetDisplay changes the target display for subsequent captures.
	// Can be called while capture is running; takes effect on the next frame.
	SetDisplay(index int) error
}
