// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"image"
	"runtime"
	"sync"
	"sync/atomic"
)

// rgbaToI420 converts an RGBA image into the planar I420 buffer the VP8 encoder
// expects: a full-resolution luma plane followed by two half-resolution chroma
// planes.
//
// The coefficients are BT.601 studio range, which is what VP8 content is
// normally interpreted as. Chroma is produced by averaging each 2x2 luma
// neighbourhood's colour before conversion rather than by subsampling one pixel
// of the four, which matters for screen content: single-pixel-wide coloured
// text is common, and point sampling drops half of it.
//
// The destination buffer must be width*height*3/2 bytes. width and height must
// be even and no larger than the image.
func rgbaToI420(dst []byte, img *image.RGBA, width, height int) {
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	yPlane := dst[:ySize]
	uPlane := dst[ySize : ySize+cSize]
	vPlane := dst[ySize+cSize : ySize+2*cSize]

	b := img.Bounds()
	at := func(x, y int) (r, g, bl int) {
		i := img.PixOffset(b.Min.X+x, b.Min.Y+y)
		return int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
	}

	// Two source rows per unit of work: the chroma pass reads a 2x2
	// neighbourhood, so pairing the rows keeps each unit self-contained and
	// every write lands in a range no other unit touches.
	forEachRowPair(height, width*height, func(y int) {
		row0 := y * width
		row1 := row0 + width
		for x := 0; x < width; x++ {
			r, g, bl := at(x, y)
			// Y = 0.257R + 0.504G + 0.098B + 16, in 16.8 fixed point.
			yPlane[row0+x] = clampByte((66*r + 129*g + 25*bl + 128 + 4096) >> 8)

			r, g, bl = at(x, y+1)
			yPlane[row1+x] = clampByte((66*r + 129*g + 25*bl + 128 + 4096) >> 8)
		}

		for x := 0; x < width; x += 2 {
			r0, g0, b0 := at(x, y)
			r1, g1, b1 := at(x+1, y)
			r2, g2, b2 := at(x, y+1)
			r3, g3, b3 := at(x+1, y+1)
			r := (r0 + r1 + r2 + r3 + 2) / 4
			g := (g0 + g1 + g2 + g3 + 2) / 4
			bl := (b0 + b1 + b2 + b3 + 2) / 4

			i := (y/2)*(width/2) + x/2
			uPlane[i] = clampByte((-38*r - 74*g + 112*bl + 128 + 32768) >> 8)
			vPlane[i] = clampByte((112*r - 94*g - 18*bl + 128 + 32768) >> 8)
		}
	})
}

// parallelPixelThreshold is the pixel count below which the conversion stays on
// one goroutine. Around 720p and up it pays for itself; below that the
// scheduling costs more than it saves.
var parallelPixelThreshold = 512 * 512

// forEachRowPair runs fn for each even source row, in parallel above a size
// worth splitting.
//
// The conversion is a pure function of the source image, and each unit writes
// only the two luma rows and the one chroma row derived from its own pair, so
// the output does not depend on the order the units complete in.
func forEachRowPair(height, pixels int, fn func(y int)) {
	pairs := height / 2
	workers := runtime.GOMAXPROCS(0)
	if workers > pairs {
		workers = pairs
	}
	if workers < 2 || pixels < parallelPixelThreshold {
		for y := 0; y+1 < height; y += 2 {
			fn(y)
		}
		return
	}

	var next atomic.Int64
	next.Store(-1)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				p := int(next.Add(1))
				if p >= pairs {
					return
				}
				fn(p * 2)
			}
		}()
	}
	wg.Wait()
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
