package vp8

import "testing"

func BenchmarkKeyFrame1080p(b *testing.B) {
	const w, h = 1920, 1080
	src := benchFrame(w, h, 0)
	enc, err := NewEncoder(w, h, 30)
	if err != nil {
		b.Fatal(err)
	}
	enc.SetKeyFrameInterval(0) // every frame a key frame
	enc.SetScreenContentProfile(true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(src); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInterFrame1080p measures the production case: a 1080p inter frame
// where a small region changed and the caller supplied a dirty map.
func BenchmarkInterFrame1080p(b *testing.B) {
	const w, h = 1920, 1080
	// Rounded up, not divided: 1080 is not a multiple of 16, and a map sized
	// h/16 is silently the wrong length and silently ignored.
	mbW, mbH := (w+15)/16, (h+15)/16

	enc, err := NewEncoder(w, h, 30)
	if err != nil {
		b.Fatal(err)
	}
	enc.SetKeyFrameInterval(0)
	enc.SetScreenContentProfile(true)
	enc.SetKeyFrameInterval(1 << 20)
	if _, err := enc.Encode(benchFrame(w, h, 0)); err != nil {
		b.Fatal(err)
	}

	// A changed band eight macroblocks wide and two tall, as a moving window or
	// a line of text would produce.
	dirty := make([]bool, mbW*mbH)
	for y := 2; y < 4; y++ {
		for x := 2; x < 10; x++ {
			dirty[y*mbW+x] = true
		}
	}

	frames := make([][]byte, 8)
	for i := range frames {
		frames[i] = benchFrame(w, h, i+1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.SetDirtyMacroblocks(dirty)
		if _, err := enc.Encode(frames[i%len(frames)]); err != nil {
			b.Fatal(err)
		}
	}
}

// benchFrame draws text-like screen content with a region that moves between
// frames, so both the key-frame and inter-frame paths get real work.
func benchFrame(w, h, idx int) []byte {
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
