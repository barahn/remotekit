package screen

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"
)

func TestVP8Encoder(t *testing.T) {
	enc := NewVP8Encoder(30, 70)
	if enc == nil {
		t.Fatal("expected non-nil VP8Encoder")
	}

	// Create test frame 1 (Solid Blue)
	img1 := image.NewRGBA(image.Rect(0, 0, 640, 480))
	draw.Draw(img1, img1.Bounds(), &image.Uniform{color.RGBA{R: 0, G: 0, B: 255, A: 255}}, image.Point{}, draw.Src)
	frame1 := &Frame{
		Image:        img1,
		Bounds:       img1.Bounds(),
		CapturedAt:   time.Now(),
		SequenceNum:  1,
		DisplayIndex: 0,
	}

	sample1, err := enc.Encode(frame1)
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}
	if len(sample1) <= 10 {
		t.Fatalf("expected sample length > 10, got %d", len(sample1))
	}

	// First 3 bytes of VP8 keyframe tag and 3 bytes start code (0x9D 0x01 0x2A)
	if sample1[3] != 0x9D || sample1[4] != 0x01 || sample1[5] != 0x2A {
		t.Fatalf("invalid VP8 start code in header: %x %x %x", sample1[3], sample1[4], sample1[5])
	}

	// Encode same frame again immediately (should return nil due to no diff / rate limit)
	sample2, err := enc.Encode(frame1)
	if err != nil {
		t.Fatalf("unexpected error on identical frame: %v", err)
	}
	if sample2 != nil {
		t.Fatalf("expected nil sample for identical frame diff, got %d bytes", len(sample2))
	}

	// Create modified frame (Dirty)
	time.Sleep(50 * time.Millisecond)
	img3 := image.NewRGBA(image.Rect(0, 0, 640, 480))
	draw.Draw(img3, img3.Bounds(), &image.Uniform{color.RGBA{R: 255, G: 0, B: 0, A: 255}}, image.Point{}, draw.Src)
	frame3 := &Frame{
		Image:        img3,
		Bounds:       img3.Bounds(),
		CapturedAt:   time.Now(),
		SequenceNum:  2,
		DisplayIndex: 0,
	}

	sample3, err := enc.Encode(frame3)
	if err != nil {
		t.Fatalf("unexpected encode error on changed frame: %v", err)
	}
	if len(sample3) <= 10 {
		t.Fatalf("expected sample length > 10 for modified frame, got %d", len(sample3))
	}
}

func TestDetectDisplayServer(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	if ds := DetectDisplayServer(); ds != DisplayServerWayland {
		t.Fatalf("expected wayland, got %s", ds)
	}

	t.Setenv("XDG_SESSION_TYPE", "x11")
	if ds := DetectDisplayServer(); ds != DisplayServerX11 {
		t.Fatalf("expected x11, got %s", ds)
	}

	t.Setenv("XDG_SESSION_TYPE", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	if ds := DetectDisplayServer(); ds != DisplayServerWayland {
		t.Fatalf("expected wayland via WAYLAND_DISPLAY, got %s", ds)
	}

	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	if ds := DetectDisplayServer(); ds != DisplayServerX11 {
		t.Fatalf("expected x11 via DISPLAY, got %s", ds)
	}
}
