// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package screen

import (
	"context"
	"errors"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/barahn/remotekit/permission"
)

// These run against the real ScreenCaptureKit. Which of the permission tests
// runs depends on whether the test binary holds Screen Recording: run from a
// terminal it normally does not, and the refusal is checked; run from an app
// bundle that was granted it, the frames are. Set RK_CAPTURE_PNG to a path to
// keep the first captured frame for a look.

func TestDarwinDisplays(t *testing.T) {
	c, err := NewCapturer(DefaultConfig())
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Stop()
	displays, err := c.Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	if len(displays) == 0 {
		t.Skip("no active display (asleep?)")
	}
	for _, d := range displays {
		t.Logf("[%d] %s at %v primary=%v", d.Index, d.Name, d.Bounds, d.Primary)
		if d.Bounds.Empty() {
			t.Errorf("display %d has empty bounds", d.Index)
		}
	}
	if !displays[0].Primary {
		t.Error("display 0 is not the primary display")
	}
}

func TestDarwinCaptureWithoutScreenRecording(t *testing.T) {
	if st := permission.Check(permission.ScreenRecording); st != permission.NotGranted {
		t.Skipf("Screen Recording is %v here", st)
	}
	c, err := NewCapturer(DefaultConfig())
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	err = c.Start(context.Background())
	if err == nil {
		c.Stop()
		t.Fatal("Start succeeded without Screen Recording")
	}
	var ng *permission.NotGrantedError
	if !errors.As(err, &ng) || ng.Permission != permission.ScreenRecording || !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Start() = %v, want the Screen Recording error", err)
	}
	t.Logf("refused as expected: %v", err)
}

func TestDarwinCaptureFrames(t *testing.T) {
	if st := permission.Check(permission.ScreenRecording); st != permission.Granted {
		t.Skipf("Screen Recording is %v here", st)
	}
	cfg := DefaultConfig()
	cfg.TargetFPS = 10
	c, err := NewCapturer(cfg)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	displays, err := c.Displays()
	if err != nil || len(displays) == 0 {
		t.Skipf("no display to capture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	var got int
	for f := range c.Frames() {
		got++
		if f.Bounds != displays[0].Bounds {
			t.Errorf("frame Bounds %v, want display 0's %v", f.Bounds, displays[0].Bounds)
		}
		var sum uint64
		for i := 0; i < len(f.Image.Pix); i += 4 {
			sum += uint64(f.Image.Pix[i]) + uint64(f.Image.Pix[i+1]) + uint64(f.Image.Pix[i+2])
		}
		t.Logf("frame #%d: %v pixels for %v points, mean %.1f", f.SequenceNum, f.Image.Bounds().Size(), f.Bounds.Size(),
			float64(sum)/float64(len(f.Image.Pix)/4*3))
		if got == 1 {
			if sum == 0 {
				t.Error("the first frame is entirely black")
			}
			if path := os.Getenv("RK_CAPTURE_PNG"); path != "" {
				if out, err := os.Create(path); err == nil {
					_ = png.Encode(out, f.Image)
					_ = out.Close()
				}
			}
		}
		if got == 5 {
			break
		}
	}
	if got < 5 {
		t.Fatalf("got %d frames, want 5", got)
	}
}
