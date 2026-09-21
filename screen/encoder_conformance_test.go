// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen_test

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

const (
	testWidth  = 64
	testHeight = 64
)

func requireTool(t *testing.T) string {
	t.Helper()
	path, required, err := vp8check.Availability()
	if err == nil {
		return path
	}
	if required {
		t.Fatalf("%s is required (%s is set) but unavailable: %v\n"+
			"Install it with: apt-get install vpx-tools", vp8check.ToolName, vp8check.RequiredEnv, err)
	}
	t.Skipf("skipping conformance check: %v (set %s=1 to make this fatal)", err, vp8check.RequiredEnv)
	return ""
}

// testFrame draws a pattern with a bar that walks right, so that consecutive
// frames differ enough for the differ to pass them through and little enough
// that inter coding has something to exploit.
func testFrame(idx int) *screen.Frame {
	img := image.NewRGBA(image.Rect(0, 0, testWidth, testHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 40, G: 90, B: 160, A: 255}}, image.Point{}, draw.Src)
	bar := image.Rect(idx*3, 0, idx*3+8, testHeight)
	draw.Draw(img, bar, &image.Uniform{color.RGBA{R: 230, G: 220, B: 60, A: 255}}, image.Point{}, draw.Src)
	return &screen.Frame{
		Image:        img,
		Bounds:       img.Bounds(),
		CapturedAt:   time.Now(),
		SequenceNum:  uint64(idx),
		DisplayIndex: 0,
	}
}

// TestEncoderProducesConformantStream decodes the production encoder's output
// with libvpx and checks it reconstructs the frames that went in.
//
// It replaces TestPlaceholderEncoderIsNotConformant, which asserted the
// opposite on purpose: the encoder used to emit a VP8 key frame header glued to
// a JPEG payload, and that test existed so the placeholder could not be shipped
// quietly. The placeholder is gone, so the assertion inverts.
func TestEncoderProducesConformantStream(t *testing.T) {
	tool := requireTool(t)

	enc := screen.NewVP8Encoder(30, 70)

	const count = 6
	var samples [][]byte
	var sources []*screen.Frame
	for i := 0; i < count; i++ {
		f := testFrame(i)
		sample, err := enc.Encode(f)
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		if sample == nil {
			// The rate limiter dropped it; give it room and retry once.
			time.Sleep(40 * time.Millisecond)
			if sample, err = enc.Encode(f); err != nil {
				t.Fatalf("Encode frame %d: %v", i, err)
			}
		}
		if sample == nil {
			t.Fatalf("frame %d produced no sample despite changing", i)
		}
		samples = append(samples, sample)
		sources = append(sources, f)
	}

	if samples[0][0]&1 != 0 {
		t.Fatal("the first sample is not a key frame; a viewer would have nothing to start from")
	}
	inter := 0
	for _, s := range samples[1:] {
		if s[0]&1 == 1 {
			inter++
		}
	}
	if inter == 0 {
		t.Fatal("every sample is a key frame; the inter path is not being exercised at all")
	}

	data, err := ivf.Marshal(ivf.Config{
		Width: testWidth, Height: testHeight,
		FPSNumerator: 30, FPSDenominator: 1,
	}, samples)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	decoded, err := vp8check.Decode(tool, data, testWidth, testHeight)
	if err != nil {
		t.Fatalf("the reference decoder rejected the production encoder's output: %v", err)
	}
	if len(decoded) != len(samples) {
		t.Fatalf("decoded %d frames, want %d", len(decoded), len(samples))
	}

	for i, got := range decoded {
		want := make([]byte, testWidth*testHeight*3/2)
		screen.RGBAToI420ForTest(want, sources[i].Image, testWidth, testHeight)
		psnr, err := vp8check.PSNR(got.Y, want[:testWidth*testHeight])
		if err != nil {
			t.Fatalf("frame %d: PSNR: %v", i, err)
		}
		t.Logf("frame %d: luma PSNR %.2f dB", i, psnr)
		if psnr < 30 {
			t.Errorf("frame %d reconstructed at %.2f dB, want at least 30", i, psnr)
		}
	}
}
