// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

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

	// A patterned background, not a flat colour. A solid frame compresses to
	// almost nothing whether it is coded as a key frame or an inter frame, so
	// it cannot show what inter coding is for. Screen content is detailed and
	// mostly static, and that combination is the whole case for this codec.
	img1 := desktopLikeImage(640, 480)
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
	if len(sample1) == 0 {
		t.Fatal("expected a sample for the first frame")
	}

	// The first frame must be a key frame: it is the only thing a viewer
	// joining the stream can start decoding from. Bit 0 of the frame tag is 0
	// for a key frame, and a key frame carries the start code that follows.
	if sample1[0]&1 != 0 {
		t.Errorf("first sample is not a key frame (frame tag %#02x)", sample1[0])
	}
	if sample1[3] != 0x9D || sample1[4] != 0x01 || sample1[5] != 0x2A {
		t.Errorf("invalid VP8 start code: %#02x %#02x %#02x", sample1[3], sample1[4], sample1[5])
	}

	// Encode the same frame again immediately: nothing changed, and the rate
	// limiter has not elapsed either, so nothing should be sent.
	sample2, err := enc.Encode(frame1)
	if err != nil {
		t.Fatalf("unexpected error on identical frame: %v", err)
	}
	if sample2 != nil {
		t.Fatalf("expected nil sample for an unchanged frame, got %d bytes", len(sample2))
	}

	// Change part of the picture, not all of it. Repainting an entire frame a
	// different colour is the worst case for inter coding -- every macroblock
	// has to be recoded, and the result is no smaller than a key frame. Screen
	// content is the opposite case, and it is the one worth asserting.
	// Change part of the picture, not all of it. Repainting an entire frame is
	// the worst case for inter coding -- every macroblock has to be recoded --
	// and it is not what a desktop does between two consecutive frames.
	time.Sleep(50 * time.Millisecond)
	img3 := desktopLikeImage(640, 480)
	draw.Draw(img3, image.Rect(32, 32, 160, 96), &image.Uniform{color.RGBA{R: 255, G: 0, B: 0, A: 255}}, image.Point{}, draw.Src)
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
	if len(sample3) == 0 {
		t.Fatal("expected a sample for the changed frame")
	}
	// A frame following a key frame is coded as an inter frame, which is the
	// whole point of the encoder replacing the placeholder: it should be much
	// smaller than the full refresh before it.
	if sample3[0]&1 != 1 {
		t.Errorf("second encoded frame is a key frame; the inter path is not being used")
	}
	// Compare like with like: the same picture coded as a key frame. Comparing
	// against the *first* key frame would not mean much here -- it is a solid
	// colour and costs almost nothing, so an inter frame carrying a real change
	// can legitimately be larger than it.
	fresh := NewVP8Encoder(30, 70)
	asKey, err := fresh.Encode(frame3)
	if err != nil {
		t.Fatalf("encoding the same frame as a key frame: %v", err)
	}
	if len(sample3)*2 >= len(asKey) {
		t.Errorf("inter frame (%d bytes) is not meaningfully smaller than the same picture "+
			"coded as a key frame (%d bytes), for a change covering %.1f%% of it",
			len(sample3), len(asKey), 100*float64(128*64)/float64(640*480))
	}
	t.Logf("same picture: %d bytes as a key frame, %d bytes as an inter frame", len(asKey), len(sample3))
}

// TestVP8EncoderResetForcesKeyFrame checks what Reset is for: a viewer joining
// late needs a key frame, and cannot use anything that predicts from frames it
// never received.
// desktopLikeImage draws something with the texture of a screen: a light
// background carrying regular dark marks, the way a window full of text does.
func desktopLikeImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 240, G: 240, B: 235, A: 255}}, image.Point{}, draw.Src)
	for y := 8; y < h-8; y += 16 {
		for x := 8; x < w-40; x += 12 {
			run := 4 + (x/12+y/16)%5
			draw.Draw(img, image.Rect(x, y, x+run, y+7),
				&image.Uniform{color.RGBA{R: 30, G: 30, B: 40, A: 255}}, image.Point{}, draw.Src)
		}
	}
	return img
}

func TestVP8EncoderResetForcesKeyFrame(t *testing.T) {
	enc := NewVP8Encoder(30, 70)

	newFrame := func(seq uint64, c color.RGBA) *Frame {
		img := image.NewRGBA(image.Rect(0, 0, 128, 96))
		draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
		return &Frame{Image: img, Bounds: img.Bounds(), CapturedAt: time.Now(), SequenceNum: seq}
	}

	if _, err := enc.Encode(newFrame(1, color.RGBA{R: 10, G: 20, B: 30, A: 255})); err != nil {
		t.Fatalf("first encode: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	inter, err := enc.Encode(newFrame(2, color.RGBA{R: 200, G: 20, B: 30, A: 255}))
	if err != nil {
		t.Fatalf("second encode: %v", err)
	}
	if len(inter) == 0 || inter[0]&1 != 1 {
		t.Fatalf("expected an inter frame before the reset")
	}

	enc.Reset()
	time.Sleep(50 * time.Millisecond)
	after, err := enc.Encode(newFrame(3, color.RGBA{R: 200, G: 20, B: 30, A: 255}))
	if err != nil {
		t.Fatalf("encode after reset: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("Reset should also clear the differ, so an unchanged frame is still encoded")
	}
	if after[0]&1 != 0 {
		t.Errorf("frame after Reset is not a key frame (frame tag %#02x)", after[0])
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

// TestDirtyMacroblocksCoversChangedTiles checks the conversion from the
// differ's 64-pixel tiles to 16-pixel macroblocks.
//
// The rounding has to be one-sided. A changed macroblock reported as clean is
// coded as a skip and freezes on screen until the next key frame; a clean one
// reported as changed only costs the analysis it would have had anyway. So
// every macroblock a changed tile touches must be marked, and being generous at
// the edges is correct rather than sloppy.
func TestDirtyMacroblocksCoversChangedTiles(t *testing.T) {
	const w, h = 128, 64
	mbW := w / 16

	diff := &FrameDiffResult{
		IsDirty:      true,
		ChangedTiles: []image.Rectangle{image.Rect(64, 0, 128, 64)},
	}
	mask := dirtyMacroblocks(diff, image.Rect(0, 0, w, h), w, h)
	if mask == nil {
		t.Fatal("expected a mask")
	}
	for my := 0; my < h/16; my++ {
		for mx := 0; mx < mbW; mx++ {
			want := mx >= 4 // the tile starts at x=64, which is macroblock 4
			if got := mask[my*mbW+mx]; got != want {
				t.Errorf("macroblock (%d,%d): dirty=%v, want %v", mx, my, got, want)
			}
		}
	}

	// A tile map that is empty means the differ found nothing, and the frame
	// would not have been encoded at all. Returning nil asks the encoder to
	// analyse everything, which is the safe answer rather than a mask of all
	// false — that would freeze the entire picture.
	if mask := dirtyMacroblocks(&FrameDiffResult{}, image.Rect(0, 0, w, h), w, h); mask != nil {
		t.Error("an empty tile list should ask for full analysis, not mark everything clean")
	}
	if mask := dirtyMacroblocks(nil, image.Rect(0, 0, w, h), w, h); mask != nil {
		t.Error("a nil diff should ask for full analysis")
	}

	// Tiles carry the image's own origin; macroblocks count from the coded
	// picture's top-left.
	off := image.Rect(100, 200, 228, 264)
	diffOff := &FrameDiffResult{IsDirty: true, ChangedTiles: []image.Rectangle{image.Rect(164, 200, 228, 264)}}
	maskOff := dirtyMacroblocks(diffOff, off, w, h)
	for my := 0; my < h/16; my++ {
		for mx := 0; mx < mbW; mx++ {
			want := mx >= 4
			if got := maskOff[my*mbW+mx]; got != want {
				t.Errorf("offset origin, macroblock (%d,%d): dirty=%v, want %v", mx, my, got, want)
			}
		}
	}
}
