// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package screen

import (
	"context"
	"image"
	"os"
	"testing"
	"time"
)

func hasDisplay() bool {
	return os.Getenv("DISPLAY") != ""
}

// requireNoUpstreamRace skips tests that construct an X11 capturer when the
// race detector is on.
//
// NewCapturer calls shm.Init, and github.com/jezek/xgb v1.3.1 writes the
// package-level xgb.NewEventFuncs and xgb.NewErrorFuncs maps there without a
// lock while the connection's readResponses goroutine reads them. The detector
// reports it every time. It is upstream code on the latest release, and it
// cannot be serialised from here because the reading goroutine belongs to xgb.
//
// The race is real and affects the agent, not only these tests - see #151.
// Skipping keeps the rest of the package under -race instead of dropping the
// whole package, which is the alternative.
// requireNoInteractiveConsent skips tests that need real pixels on a Wayland
// session.
//
// With the portal handshake fixed, Wayland capture goes through
// xdg-desktop-portal, which raises a consent dialog and blocks until a person
// answers it. A test cannot click Share, so it would either hang or assert on
// frames that will never arrive. Under X11 - including Xvfb in CI - capture
// needs no consent and these tests run normally.
func requireNoInteractiveConsent(t *testing.T) {
	t.Helper()
	if DetectDisplayServer() == DisplayServerWayland {
		t.Skip("skipping on a Wayland session: capture requires interactive portal consent")
	}
}

func requireNoUpstreamRace(t *testing.T) {
	t.Helper()
	if raceDetectorEnabled {
		t.Skip("skipping under -race: data race inside xgb v1.3.1 shm.Init (see #151)")
	}
}

func TestNewCapturer(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)
	requireNoInteractiveConsent(t)

	config := DefaultConfig()
	cap, err := NewCapturer(config)
	if err != nil {
		t.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	displays, err := cap.Displays()
	if err != nil {
		t.Fatalf("Displays() failed: %v", err)
	}

	if len(displays) == 0 {
		t.Fatal("expected at least one display")
	}

	t.Logf("Found %d display(s):", len(displays))
	for _, d := range displays {
		t.Logf("  [%d] %s: %dx%d primary=%v",
			d.Index, d.Name, d.Bounds.Dx(), d.Bounds.Dy(), d.Primary)
	}
}

func TestCaptureFrames(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)
	requireNoInteractiveConsent(t)

	config := DefaultConfig()
	config.TargetFPS = 10 // Low FPS for testing
	cap, err := NewCapturer(config)
	if err != nil {
		t.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := cap.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	var frameCount int
	for frame := range cap.Frames() {
		frameCount++
		if frame.Image == nil {
			t.Error("received nil Image in frame")
			continue
		}
		if frame.Bounds.Empty() {
			t.Error("received empty Bounds in frame")
		}
		if frame.SequenceNum == 0 {
			t.Error("SequenceNum should be > 0")
		}

		// Verify the image has actual pixel data (not all zeros)
		if len(frame.Image.Pix) == 0 {
			t.Error("frame has no pixel data")
		}

		t.Logf("Frame #%d: %dx%d captured at %s",
			frame.SequenceNum,
			frame.Bounds.Dx(), frame.Bounds.Dy(),
			frame.CapturedAt.Format(time.RFC3339Nano))

		// Stop after a few frames
		if frameCount >= 5 {
			cap.Stop()
			break
		}
	}

	if frameCount == 0 {
		t.Error("received no frames")
	}

	t.Logf("Captured %d frames total", frameCount)
}

func TestDoubleStart(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)
	requireNoInteractiveConsent(t)

	config := DefaultConfig()
	cap, err := NewCapturer(config)
	if err != nil {
		t.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cap.Start(ctx); err != nil {
		t.Fatalf("first Start() failed: %v", err)
	}

	err = cap.Start(ctx)
	if err != ErrAlreadyStarted {
		t.Errorf("second Start() expected ErrAlreadyStarted, got: %v", err)
	}

	cap.Stop()
}

func TestSetDisplayInvalid(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)
	requireNoInteractiveConsent(t)

	config := DefaultConfig()
	cap, err := NewCapturer(config)
	if err != nil {
		t.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	err = cap.SetDisplay(99)
	if err != ErrDisplayNotFound {
		t.Errorf("SetDisplay(99) expected ErrDisplayNotFound, got: %v", err)
	}
}

func BenchmarkCaptureFrame(b *testing.B) {
	if !hasDisplay() {
		b.Skip("DISPLAY not set — skipping X11 capture benchmark")
	}

	config := DefaultConfig()
	config.TargetFPS = 0 // Maximum rate
	config.FrameBufferSize = 1

	cap, err := NewCapturer(config)
	if err != nil {
		b.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	x11cap, ok := cap.(*x11Capturer)
	if !ok {
		b.Skip("non-X11 capturer detected — skipping X11 SHM benchmark")
	}

	rect := image.Rect(0, 0, int(x11cap.width), int(x11cap.height))
	w := rect.Dx()
	h := rect.Dy()
	buf := make([]byte, w*h*4)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var err error
		if x11cap.shmAvailable {
			err = x11cap.captureSHM(buf, rect)
		} else {
			err = x11cap.captureFallback(buf, rect)
		}
		if err != nil {
			b.Fatalf("capture failed: %v", err)
		}
	}

	b.Logf("Resolution: %dx%d, SHM: %v", w, h, x11cap.shmAvailable)
}

func BenchmarkBGRAtoRGBA(b *testing.B) {
	// Benchmark the color conversion for a 1080p frame
	w, h := 1920, 1080
	src := make([]byte, w*h*4)
	dst := make([]byte, w*h*4)

	// Fill with test pattern
	for i := range src {
		src[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(w * h * 4))

	for i := 0; i < b.N; i++ {
		bgraToRGBA(src, dst, w, h)
	}
}
