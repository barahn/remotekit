// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
// Forked from github.com/opd-ai/vp8 (MIT); see LICENSE.upstream.

package vp8

import (
	"fmt"
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// TestConformanceAcrossContentAndSize decodes content the rest of the suite
// never produced, at sizes it never reached.
//
// Every byte-exact test in this package used to encode one 64x64 source
// pattern, and that pattern makes the mode decision choose B_PRED for every
// macroblock. Flat regions choose 16x16 modes instead — which is most of a real
// desktop, and which went entirely untested. Nothing larger than 64x64 had ever
// been decoded through libvpx either: the benchmarks measure speed and decode
// nothing.
//
// Four defects hid in that gap, all of them silent — the bitstream stayed
// parseable and the picture was simply wrong. Every case here passes now, and
// the test is kept so the gap cannot reopen.
func TestConformanceAcrossContentAndSize(t *testing.T) {
	tool, _, err := vp8check.Availability()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}

	sources := map[string]func(w, h, idx int) []byte{
		"flat":     flatQuadrantSource,
		"textured": textLikeSource,
	}
	sizes := []struct{ w, h int }{{64, 64}, {320, 240}, {1280, 720}}

	for name, src := range sources {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", name, size.w, size.h), func(t *testing.T) {
				// 720p is most of this test's time under -race and adds no
				// concurrency; CI runs it in full in a step without -race.
				if testing.Short() && size.w >= 1280 {
					t.Skip("720p takes tens of seconds under -race; skipped under -short")
				}
				enc, err := NewEncoder(size.w, size.h, 30)
				if err != nil {
					t.Fatal(err)
				}
				enc.SetKeyFrameInterval(6)
				enc.SetScreenContentProfile(true)

				const count = 3
				var frames [][]byte
				var recon [][]byte
				for i := 0; i < count; i++ {
					b, err := enc.Encode(src(size.w, size.h, i))
					if err != nil {
						t.Fatal(err)
					}
					frames = append(frames, b)
					recon = append(recon, append([]byte(nil), enc.refFrames.last.Y...))
				}

				data, err := ivf.Marshal(ivf.Config{
					Width: uint16(size.w), Height: uint16(size.h),
					FPSNumerator: 30, FPSDenominator: 1,
				}, frames)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := vp8check.Decode(tool, data, size.w, size.h)
				if err != nil {
					t.Fatalf("the reference decoder rejected the stream: %v", err)
				}
				for i := range decoded {
					differing, maxAbs := rdDiff(recon[i], decoded[i].Y)
					if differing != 0 {
						t.Errorf("frame %d: the encoder's reference disagrees with the decoder on "+
							"%d of %d luma bytes (max |delta| %d)", i, differing, len(recon[i]), maxAbs)
					}
				}
			})
		}
	}
}

// flatQuadrantSource is four flat regions plus a walking bar: the mode decision
// picks 16x16 modes and skips most macroblocks, which the textured pattern
// never does.
func flatQuadrantSource(w, h, idx int) []byte {
	buf := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v byte
			switch {
			case x < w/2 && y < h/2:
				v = 40
			case x >= w/2 && y < h/2:
				v = 110
			case x < w/2 && y >= h/2:
				v = 180
			default:
				v = 235
			}
			if x >= idx*12 && x < idx*12+24 {
				v = 16
			}
			buf[y*w+x] = v
		}
	}
	for i := w * h; i < len(buf); i++ {
		buf[i] = 128
	}
	return buf
}

// textLikeSource is dense dark marks on a light ground, as a window full of
// text looks to the encoder.
func textLikeSource(w, h, idx int) []byte {
	buf := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 240
			if (x/12+y/16)%3 == 0 && x%12 < 6 && y%16 < 7 {
				v = 30
			}
			if x >= idx*4 && x < idx*4+24 && y < 32 {
				v = 120
			}
			buf[y*w+x] = byte(v)
		}
	}
	for i := w * h; i < len(buf); i++ {
		buf[i] = byte(100 + (i % 40))
	}
	return buf
}
