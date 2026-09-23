// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package screen

import (
	"context"
	"image"
	"testing"
	"time"
)

// TestMonitorEnumeration checks that every enumerated monitor is usable: a
// non-empty rectangle, a distinct index, and accepted by SetDisplay.
//
// It runs on a single-monitor host too, where it covers the RANDR-absent
// fallback path.
func TestMonitorEnumeration(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)

	c, err := newX11Capturer(DefaultConfig())
	if err != nil {
		t.Fatalf("newX11Capturer failed: %v", err)
	}
	defer c.Close()

	displays, err := c.Displays()
	if err != nil {
		t.Fatalf("Displays() failed: %v", err)
	}
	if len(displays) == 0 {
		t.Fatal("expected at least one monitor")
	}

	t.Logf("randr=%v, X screen %dx%d, %d monitor(s)", c.randrReady, c.width, c.height, len(displays))

	seen := make(map[int]bool, len(displays))
	for _, d := range displays {
		t.Logf("  [%d] %s %dx%d at (%d,%d) primary=%v",
			d.Index, d.Name, d.Bounds.Dx(), d.Bounds.Dy(), d.Bounds.Min.X, d.Bounds.Min.Y, d.Primary)

		if d.Bounds.Empty() {
			t.Errorf("monitor %d has an empty rectangle", d.Index)
		}
		if seen[d.Index] {
			t.Errorf("monitor index %d appears twice", d.Index)
		}
		seen[d.Index] = true

		if err := c.SetDisplay(d.Index); err != nil {
			t.Errorf("SetDisplay(%d) failed for an enumerated monitor: %v", d.Index, err)
		}
	}

	// One past the end must be refused rather than silently falling back to
	// the primary monitor, or the operator sees a monitor they did not pick.
	if err := c.SetDisplay(len(displays)); err != ErrDisplayNotFound {
		t.Errorf("SetDisplay(%d) = %v, want ErrDisplayNotFound", len(displays), err)
	}
	if err := c.SetDisplay(-1); err != ErrDisplayNotFound {
		t.Errorf("SetDisplay(-1) = %v, want ErrDisplayNotFound", err)
	}
}

// TestCaptureIsCroppedToSelectedMonitor is the regression guard for the whole
// point of the feature: capture must yield ONE monitor, not the spanned X
// screen. Before per-monitor capture, three 1080p monitors were streamed as a
// single 3840x2160 frame.
//
// Skipped on a single-monitor host, where there is nothing to crop.
func TestCaptureIsCroppedToSelectedMonitor(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 capture test")
	}
	requireNoUpstreamRace(t)
	requireNoInteractiveConsent(t)

	cfg := DefaultConfig()
	cfg.TargetFPS = 10
	c, err := newX11Capturer(cfg)
	if err != nil {
		t.Fatalf("newX11Capturer failed: %v", err)
	}
	defer c.Close()

	displays, err := c.Displays()
	if err != nil {
		t.Fatalf("Displays() failed: %v", err)
	}
	if len(displays) < 2 {
		t.Skip("single-monitor host — nothing to crop")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer c.Stop()

	fingerprints := make(map[int]uint64, len(displays))

	for _, d := range displays {
		if err := c.SetDisplay(d.Index); err != nil {
			t.Fatalf("SetDisplay(%d) failed: %v", d.Index, err)
		}

		// The switch takes effect on the next frame, and frames already in
		// flight belong to the previous monitor, so drain a few.
		var frame *Frame
		for n := 0; n < 5; n++ {
			select {
			case f := <-c.Frames():
				frame = f
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for a frame on monitor %d", d.Index)
			}
		}
		if frame == nil || frame.Image == nil {
			t.Fatalf("no frame for monitor %d", d.Index)
		}

		got := frame.Image.Bounds()
		if got.Dx() != d.Bounds.Dx() || got.Dy() != d.Bounds.Dy() {
			t.Errorf("monitor %d: captured %dx%d, want %dx%d (the whole X screen is %dx%d)",
				d.Index, got.Dx(), got.Dy(), d.Bounds.Dx(), d.Bounds.Dy(), c.width, c.height)
		}
		// Frame.Bounds carries the monitor's GLOBAL rectangle; input injection
		// needs that offset to aim at the monitor being viewed.
		if frame.Bounds != d.Bounds {
			t.Errorf("monitor %d: Frame.Bounds = %v, want the global rect %v",
				d.Index, frame.Bounds, d.Bounds)
		}
		if frame.DisplayIndex != d.Index {
			t.Errorf("monitor %d: Frame.DisplayIndex = %d", d.Index, frame.DisplayIndex)
		}

		var h uint64 = 14695981039346656037
		for _, b := range frame.Image.Pix {
			h = (h ^ uint64(b)) * 1099511628211
		}
		fingerprints[d.Index] = h
	}

	// Identical pixels across two monitors would mean the crop never moved and
	// the same region was grabbed each time.
	for i := range displays {
		for j := i + 1; j < len(displays); j++ {
			a, b := displays[i].Index, displays[j].Index
			if fingerprints[a] == fingerprints[b] {
				t.Errorf("monitors %d and %d captured identical pixels — the crop is not being applied", a, b)
			}
		}
	}
}

// TestX11RectRejectsOutOfRange covers the narrowing to the X11 protocol's
// int16 coordinates and uint16 dimensions. A silent wrap here would grab a
// region somewhere else on the screen and stream it as if it were the monitor
// the operator picked.
func TestX11RectRejectsOutOfRange(t *testing.T) {
	tests := []struct {
		name    string
		rect    image.Rectangle
		wantErr bool
	}{
		{"typical monitor", image.Rect(1920, 0, 3840, 1080), false},
		{"origin", image.Rect(0, 0, 1920, 1080), false},
		{"negative origin", image.Rect(-1920, -1080, 0, 0), false},
		{"largest addressable origin", image.Rect(32767, 32767, 32768, 32768), false},
		{"origin past int16", image.Rect(32768, 0, 33000, 1080), true},
		{"y origin past int16", image.Rect(0, 32768, 1920, 33848), true},
		{"origin below int16", image.Rect(-32770, 0, -32000, 1080), true},
		{"width past uint16", image.Rect(0, 0, 65536, 1080), true},
		{"height past uint16", image.Rect(0, 0, 1920, 65536), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y, w, h, err := x11Rect(tc.rect)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("x11Rect(%v) = (%d,%d,%d,%d), nil; want an error", tc.rect, x, y, w, h)
				}
				return
			}
			if err != nil {
				t.Fatalf("x11Rect(%v) returned %v", tc.rect, err)
			}
			if int(x) != tc.rect.Min.X || int(y) != tc.rect.Min.Y {
				t.Errorf("origin round-tripped as (%d,%d), want (%d,%d)", x, y, tc.rect.Min.X, tc.rect.Min.Y)
			}
			if int(w) != tc.rect.Dx() || int(h) != tc.rect.Dy() {
				t.Errorf("size round-tripped as %dx%d, want %dx%d", w, h, tc.rect.Dx(), tc.rect.Dy())
			}
		})
	}
}
