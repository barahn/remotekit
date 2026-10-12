// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8_test

import (
	"fmt"
	"image"
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// panningFrame is a textured scene that slides diagonally, with a block of
// detail that changes every frame: motion for the inter path, edges and noise
// for B_PRED, and flat areas that skip.
func panningFrame(w, h, idx int) []byte {
	buf := make([]byte, w*h*3/2)
	seed := uint32(idx*7919 + 1)
	rnd := func() uint32 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return seed
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := x+2*idx, y+idx
			v := (sx*3+sy*5)%97 + (sx/12+sy/12)%2*100
			if x > w/3 && x < w/3+20 && y > h/3 && y < h/3+20 {
				v = int(rnd() % 256)
			}
			buf[y*w+x] = byte(v)
		}
	}
	cw, ch := w/2, h/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			buf[w*h+y*cw+x] = byte(128 + (x+idx)%40 - 20)
			buf[w*h+cw*ch+y*cw+x] = byte(128 + (y+2*idx)%30 - 15)
		}
	}
	return buf
}

type decodeCase struct {
	name       string
	w, h       int
	frames     int
	keyEvery   int
	configure  func(*vp8.Encoder) error
	frameAtIdx func(w, h, idx int) []byte
}

var decodeCases = []decodeCase{
	{name: "64x64 defaults", w: 64, h: 64, frames: 12, keyEvery: 0},
	{name: "176x144 pan", w: 176, h: 144, frames: 20, keyEvery: 300},
	{name: "176x144 loop filter and partitions", w: 176, h: 144, frames: 15, keyEvery: 300,
		configure: func(e *vp8.Encoder) error {
			e.SetLoopFilterLevel(40)
			return e.SetPartitionCount(vp8.FourPartitions)
		}},
	{name: "160x96 golden and quant deltas", w: 160, h: 96, frames: 16, keyEvery: 8,
		configure: func(e *vp8.Encoder) error {
			e.SetGoldenFrameInterval(4)
			e.SetQuantizerDeltas(2, -3, 4, -2, 3)
			e.SetProbabilityUpdates(true)
			return nil
		}},
	{name: "100x70, not whole macroblocks", w: 100, h: 70, frames: 10, keyEvery: 300},
	{name: "conformance pattern", w: confWidth, h: confHeight, frames: 10, keyEvery: 300,
		frameAtIdx: func(_, _, idx int) []byte { return sourceFrame(idx) }},
}

// TestDecoderMatchesLibvpx decodes what the encoder produces and holds every
// frame to vpxdec's output, byte for byte.
func TestDecoderMatchesLibvpx(t *testing.T) {
	tool := requireTool(t)
	for _, tc := range decodeCases {
		t.Run(tc.name, func(t *testing.T) {
			frames := encodeCase(t, tc)
			want := oracleDecode(t, tool, tc.w, tc.h, frames)
			if len(want) != len(frames) {
				t.Fatalf("vpxdec decoded %d frames of %d", len(want), len(frames))
			}
			dec := vp8.NewDecoder()
			for i, f := range frames {
				img, err := dec.Decode(f)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if img == nil {
					t.Fatalf("frame %d: not shown", i)
				}
				if err := samePicture(img, want[i]); err != nil {
					t.Fatalf("frame %d (key %v): %v", i, f[0]&1 == 0, err)
				}
			}
		})
	}
}

func encodeCase(t *testing.T, tc decodeCase) [][]byte {
	t.Helper()
	enc, err := vp8.NewEncoder(tc.w, tc.h, 30)
	if err != nil {
		t.Fatal(err)
	}
	enc.SetKeyFrameInterval(tc.keyEvery)
	if tc.configure != nil {
		if err := tc.configure(enc); err != nil {
			t.Fatal(err)
		}
	}
	src := tc.frameAtIdx
	if src == nil {
		src = panningFrame
	}
	out := make([][]byte, tc.frames)
	for i := range out {
		if out[i], err = enc.Encode(src(tc.w, tc.h, i)); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
	}
	return out
}

func oracleDecode(t *testing.T, tool string, w, h int, frames [][]byte) []vp8check.Frame {
	t.Helper()
	data, err := ivf.Marshal(ivf.Config{Width: uint16(w), Height: uint16(h), FPSNumerator: 30, FPSDenominator: 1}, frames)
	if err != nil {
		t.Fatal(err)
	}
	got, err := vp8check.Decode(tool, data, w, h)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// samePicture compares a decoded image with vpxdec's planes, and says where
// the first difference is.
func samePicture(img *image.YCbCr, want vp8check.Frame) error {
	w, h := want.Width, want.Height
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Errorf("size %v, want %dx%d", img.Rect.Size(), w, h)
	}
	planes := []struct {
		name   string
		got    []byte
		stride int
		want   []byte
		w, h   int
	}{
		{"Y", img.Y, img.YStride, want.Y, w, h},
		{"U", img.Cb, img.CStride, want.U, w / 2, h / 2},
		{"V", img.Cr, img.CStride, want.V, w / 2, h / 2},
	}
	for _, p := range planes {
		diffs, first := 0, ""
		for y := 0; y < p.h; y++ {
			for x := 0; x < p.w; x++ {
				g, e := p.got[y*p.stride+x], p.want[y*p.w+x]
				if g != e {
					if diffs == 0 {
						first = fmt.Sprintf("(%d,%d) = %d, want %d (macroblock %d,%d)", x, y, g, e, x/(16*w/p.w), y/(16*h/p.h))
					}
					diffs++
				}
			}
		}
		if diffs > 0 {
			return fmt.Errorf("%s plane: %d pixels differ, first at %s", p.name, diffs, first)
		}
	}
	return nil
}

// Frames come off the network, so a corrupt one must fail or decode to
// garbage, never panic. Every prefix and a spread of bit flips of a real
// stream are fed in, each after a good key frame.
//
// That is some 28,000 feeds. Under -race with coverage they take far longer
// than the CI job allows and the single-goroutine decoder gives the detector
// nothing to find, so -short samples every 37th prefix and flip; CI runs the
// full sweep in a step without -race.
func TestDecoderSurvivesCorruptFrames(t *testing.T) {
	step := 1
	if testing.Short() {
		step = 37
	}
	frames := encodeCase(t, decodeCase{w: 64, h: 48, frames: 4, keyEvery: 300})
	feed := func(data []byte) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on a corrupt frame of %d bytes: %v", len(data), r)
			}
		}()
		dec := vp8.NewDecoder()
		_, _ = dec.Decode(frames[0])
		_, _ = dec.Decode(data)
		_, _ = dec.Decode(frames[1])
	}
	for _, f := range frames {
		for n := 0; n < len(f); n += step {
			feed(f[:n])
		}
		for i := 0; i < len(f)*8; i += 3 * step {
			bad := append([]byte(nil), f...)
			bad[i/8] ^= 1 << uint(i%8)
			feed(bad)
		}
	}
}

func FuzzDecoder(f *testing.F) {
	enc, err := vp8.NewEncoder(48, 32, 30)
	if err != nil {
		f.Fatal(err)
	}
	src := make([]byte, 48*32*3/2)
	for i := 0; i < 3; i++ {
		for j := range src {
			src[j] = byte(j*(i+3) + i)
		}
		frame, err := enc.Encode(src)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(frame)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// A key frame may declare up to 8K UHD, which is slow to decode
		// and proves nothing more than a small one: keep the fuzzer on
		// small pictures, where it finds panics rather than timeouts.
		if len(data) >= 10 && data[0]&1 == 0 {
			w := int(data[6]) | int(data[7]&0x3f)<<8
			h := int(data[8]) | int(data[9]&0x3f)<<8
			if w*h > 256*256 {
				return
			}
		}
		dec := vp8.NewDecoder()
		_, _ = dec.Decode(data)
		_, _ = dec.Decode(data)
	})
}

// BenchmarkDecode1080p decodes a screen-sized stream: a key frame, then inter
// frames of a panning scene.
func BenchmarkDecode1080p(b *testing.B) {
	const w, h = 1920, 1080
	enc, err := vp8.NewEncoder(w, h, 30)
	if err != nil {
		b.Fatal(err)
	}
	enc.SetKeyFrameInterval(300)
	var frames [][]byte
	for i := 0; i < 6; i++ {
		f, err := enc.Encode(panningFrame(w, h, i))
		if err != nil {
			b.Fatal(err)
		}
		frames = append(frames, f)
	}
	b.Run("key", func(b *testing.B) {
		dec := vp8.NewDecoder()
		for i := 0; i < b.N; i++ {
			if _, err := dec.Decode(frames[0]); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("inter", func(b *testing.B) {
		dec := vp8.NewDecoder()
		if _, err := dec.Decode(frames[0]); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// The same inter frame each time, against a last that moves on:
			// garbage after the first, but the same work.
			if _, err := dec.Decode(frames[1+i%5]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// desktopFrame is closer to what a screen stream carries: flat panels, rows
// of glyph-like strokes, and a small region that changes, like a cursor or a
// line being typed.
func desktopFrame(w, h, idx int) []byte {
	buf := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 235
			switch {
			case y < 40:
				v = 60 // a title bar
			case x < 240:
				v = 200 // a side panel
			}
			// Text: short strokes on a 9x18 grid, in the content area.
			if y >= 60 && x >= 260 && y%18 < 12 && (x/9*7+y/18*13)%5 != 0 && (x%9 == 2 || (y%18 == 5 && x%9 < 6)) {
				v = 30
			}
			buf[y*w+x] = byte(v)
		}
	}
	// The line being typed grows by one glyph a frame.
	ty := 60 + 18*20
	for x := 260; x < 260+9*idx && x < w; x++ {
		for y := ty; y < ty+12; y++ {
			if x%9 == 3 || y == ty+6 {
				buf[y*w+x] = 30
			}
		}
	}
	for i := w * h; i < len(buf); i++ {
		buf[i] = 128
	}
	return buf
}

func BenchmarkDecodeDesktop1080p(b *testing.B) {
	const w, h = 1920, 1080
	enc, err := vp8.NewEncoder(w, h, 30)
	if err != nil {
		b.Fatal(err)
	}
	enc.SetKeyFrameInterval(300)
	var frames [][]byte
	for i := 0; i < 31; i++ {
		f, err := enc.Encode(desktopFrame(w, h, i))
		if err != nil {
			b.Fatal(err)
		}
		frames = append(frames, f)
	}
	b.Logf("key frame %d bytes, inter frames about %d bytes", len(frames[0]), len(frames[1]))
	b.Run("key", func(b *testing.B) {
		dec := vp8.NewDecoder()
		for i := 0; i < b.N; i++ {
			if _, err := dec.Decode(frames[0]); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("inter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			dec := vp8.NewDecoder()
			if _, err := dec.Decode(frames[0]); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			for _, f := range frames[1:] {
				if _, err := dec.Decode(f); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*30)/1e6, "ms/frame")
	})
}
