package screen

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// TestConversionIsBitExactInParallel holds the parallel colour conversion to
// the same standard as the encoder's parallel analysis pass: splitting the work
// across goroutines may not change a single output byte.
//
// The conversion writes three planes from one source, and the chroma pass reads
// a 2x2 neighbourhood, so the row pairing is what keeps each unit's writes
// disjoint. A mistake there would show up as output that varies with worker
// timing — which is why this compares the two paths rather than reasoning about
// them.
func TestConversionIsBitExactInParallel(t *testing.T) {
	const w, h = 640, 480

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*7 + y*3) % 256),
				G: uint8((x*3 + y*11) % 256),
				B: uint8((x*13 + y*5) % 256),
				A: 255,
			})
		}
	}

	convert := func(threshold int) []byte {
		saved := parallelPixelThreshold
		parallelPixelThreshold = threshold
		defer func() { parallelPixelThreshold = saved }()

		out := make([]byte, w*h*3/2)
		rgbaToI420(out, img, w, h)
		return out
	}

	serial := convert(1 << 30)
	parallel := convert(1)

	if !bytes.Equal(serial, parallel) {
		differing := 0
		for i := range serial {
			if serial[i] != parallel[i] {
				differing++
			}
		}
		t.Fatalf("%d of %d bytes differ between the serial and parallel conversion", differing, len(serial))
	}
}

// TestConversionCoversEveryPixel guards the row pairing against the failure it
// invites: leaving the last row, or every other row, unwritten. A buffer that
// starts as a recognisable non-zero pattern makes an untouched byte visible.
func TestConversionCoversEveryPixel(t *testing.T) {
	for _, size := range []struct{ w, h int }{{64, 64}, {640, 480}, {1920, 1080}, {800, 600}} {
		img := image.NewRGBA(image.Rect(0, 0, size.w, size.h))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		out := make([]byte, size.w*size.h*3/2)
		for i := range out {
			out[i] = 0xAB
		}
		rgbaToI420(out, img, size.w, size.h)

		for i, b := range out {
			if b == 0xAB {
				t.Errorf("%dx%d: byte %d was never written", size.w, size.h, i)
				break
			}
		}
	}
}
