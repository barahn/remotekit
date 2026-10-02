// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import (
	"bytes"
	"testing"
)

// TestWavefrontIsBitExact is what makes the wavefront admissible at all.
//
// Running the analysis pass across goroutines is only legitimate if the result
// cannot depend on the order macroblocks finish in. Rather than argue that from
// the dependency rule, this encodes the same frames serially and in the wave and
// compares the bitstreams byte for byte. A missed dependency shows up here as a
// difference that comes and goes between runs, so both frame types and several
// frames are covered.
func TestWavefrontIsBitExact(t *testing.T) {
	sizes := []struct{ w, h int }{
		{320, 192}, // small: several rows, few columns, so rows block often
		{640, 480},
		{1280, 720},
		{1920, 1080}, // 1080 is not a multiple of 16: the last row is partial
	}

	for _, size := range sizes {
		// 720p and above add nothing the two smaller sizes do not already
		// cover, and under -race they are nearly all of this test's time.
		if testing.Short() && size.w >= 1280 {
			continue
		}
		encode := func(threshold int) [][]byte {
			saved := wavefrontThreshold
			wavefrontThreshold = threshold
			defer func() { wavefrontThreshold = saved }()

			enc, err := NewEncoder(size.w, size.h, 30)
			if err != nil {
				t.Fatal(err)
			}
			enc.SetKeyFrameInterval(3) // key, inter, inter, key, ...
			enc.SetBitrate(4_000_000)
			enc.SetRateControl(true)

			var out [][]byte
			for i := 0; i < 6; i++ {
				b, err := enc.Encode(textLikeSource(size.w, size.h, i))
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, b)
			}
			return out
		}

		serial := encode(1 << 30) // above any frame: never parallel
		wave := encode(1)         // below any frame: always parallel

		if len(serial) != len(wave) {
			t.Fatalf("%dx%d: frame counts differ", size.w, size.h)
		}
		for i := range serial {
			if !bytes.Equal(serial[i], wave[i]) {
				t.Errorf("%dx%d frame %d differs: %d bytes serial, %d bytes wavefront — "+
					"the analysis pass depends on completion order, which means the dependency "+
					"rule is wrong or incompletely enforced",
					size.w, size.h, i, len(serial[i]), len(wave[i]))
			}
		}
	}
}

// TestWavefrontWithDirtyMap covers the inter path's fast lane, where most
// macroblocks skip analysis entirely and still have to release the ones waiting
// on them.
func TestWavefrontWithDirtyMap(t *testing.T) {
	const w, h = 640, 480
	mbW, mbH := (w+15)/16, (h+15)/16

	encode := func(threshold int) [][]byte {
		saved := wavefrontThreshold
		wavefrontThreshold = threshold
		defer func() { wavefrontThreshold = saved }()

		enc, _ := NewEncoder(w, h, 30)
		enc.SetKeyFrameInterval(20)
		dirty := make([]bool, mbW*mbH)
		for y := 3; y < 8; y++ {
			for x := 5; x < 15; x++ {
				dirty[y*mbW+x] = true
			}
		}
		var out [][]byte
		for i := 0; i < 5; i++ {
			if i > 0 {
				enc.SetDirtyMacroblocks(dirty)
			}
			b, err := enc.Encode(textLikeSource(w, h, i))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		return out
	}

	serial, wave := encode(1<<30), encode(1)
	for i := range serial {
		if !bytes.Equal(serial[i], wave[i]) {
			t.Errorf("frame %d differs with a dirty map in use: %d vs %d bytes",
				i, len(serial[i]), len(wave[i]))
		}
	}
}
