package screen

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"
)

// benchDesktopFrame is 1080p screen-like content with one small region that
// moves between frames, which is the shape of a real desktop stream: dense
// static detail, a little motion.
func benchDesktopFrame(w, h, idx int) *Frame {
	img := desktopLikeImage(w, h)
	draw.Draw(img, image.Rect(40+idx*3, 40, 40+idx*3+120, 60),
		&image.Uniform{color.RGBA{R: 200, G: 40, B: 40, A: 255}}, image.Point{}, draw.Src)
	return &Frame{Image: img, Bounds: img.Bounds(), CapturedAt: time.Now(), SequenceNum: uint64(idx)}
}

// BenchmarkEncodeKeyFrame1080p measures the full refresh: colour conversion and
// a complete intra-coded frame. This is the periodic hitch in a stream, so its
// cost is a latency figure rather than a throughput one.
func BenchmarkEncodeKeyFrame1080p(b *testing.B) {
	const w, h = 1920, 1080
	frames := make([]*Frame, 4)
	for i := range frames {
		frames[i] = benchDesktopFrame(w, h, i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// A fresh encoder each iteration: Reset would also do, but a new one
		// leaves no doubt that nothing is carried across.
		enc := NewVP8Encoder(30, 70)
		f := frames[i%len(frames)]
		b.StartTimer()

		s, err := enc.Encode(f)
		if err != nil {
			b.Fatal(err)
		}
		if len(s) == 0 || s[0]&1 != 0 {
			b.Fatal("expected a key frame")
		}
	}
}

// BenchmarkEncodeInterFrame1080p measures the steady state: the frame budget at
// 30fps is 33.3ms, and this is what has to fit inside it.
func BenchmarkEncodeInterFrame1080p(b *testing.B) {
	const w, h = 1920, 1080
	frames := make([]*Frame, 8)
	for i := range frames {
		frames[i] = benchDesktopFrame(w, h, i)
	}

	enc := NewVP8Encoder(30, 70)
	if _, err := enc.Encode(frames[0]); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := frames[(i+1)%len(frames)]
		// The rate limiter drops frames offered faster than the frame
		// interval; encoding is what is being measured, not that gate.
		enc.lastEncode = time.Time{}
		s, err := enc.Encode(f)
		if err != nil {
			b.Fatal(err)
		}
		if len(s) == 0 {
			b.Fatal("frame produced no sample")
		}
	}
}
