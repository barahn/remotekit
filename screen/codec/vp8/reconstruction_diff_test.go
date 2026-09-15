package vp8

import (
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// This file compares the encoder's own reconstruction — the picture it stores
// as the `last` reference and predicts the next frame from — against what
// libvpx reconstructs from the very bitstream the encoder just produced.
//
// VP8 prediction is closed-loop, so the two must be identical. Any divergence
// means every later inter frame predicts from a picture the decoder does not
// have, and PSNR against the *source* cannot tell you which side is wrong. This
// diff can.

const (
	rdW = 64
	rdH = 64
)

// rdSource is a moving pattern with structured chroma.
//
// The chroma matters. An earlier version of this diagnostic filled both chroma
// planes with a constant 128, and the chroma planes then matched the decoder
// exactly — which looked like evidence that inter prediction was sound for
// chroma and broken only for luma. It was not evidence of anything: a flat
// plane reconstructs identically under any motion vector and any prediction
// mode. With structured chroma, both planes diverge, and the fault is visible
// for what it is.
func rdSource(idx int) []byte {
	buf := make([]byte, 0, rdW*rdH*3/2)
	for y := 0; y < rdH; y++ {
		for x := 0; x < rdW; x++ {
			v := (x/8+y/8)%2*90 + 60
			if x >= idx*3 && x < idx*3+8 {
				v = 230
			}
			buf = append(buf, byte(v))
		}
	}
	for p := 0; p < 2; p++ {
		for y := 0; y < rdH/2; y++ {
			for x := 0; x < rdW/2; x++ {
				buf = append(buf, byte(90+((x/4+y/4+p*2)%3)*50))
			}
		}
	}
	return buf
}

func rdDiff(a, b []byte) (differing, maxAbs int) {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d != 0 {
			differing++
		}
		if d > maxAbs {
			maxAbs = d
		}
	}
	return
}

// TestEncoderReferenceMatchesDecoder is the strictest check available: byte
// equality between the picture the encoder keeps as its reference and the
// picture libvpx reconstructs from the same bitstream.
//
// It has to hold for every frame. VP8 prediction is closed-loop, so a single
// byte of disagreement compounds into the next frame and the next. While the
// inter path was broken this test's inter frames disagreed on roughly 90% of
// their bytes; they now agree on all of them.
func TestEncoderReferenceMatchesDecoder(t *testing.T) {
	tool, required, err := vp8check.Availability()
	if err != nil {
		if required {
			t.Fatalf("%s is required but unavailable: %v", vp8check.ToolName, err)
		}
		t.Skipf("skipping: %v (set %s=1 to make this fatal)", err, vp8check.RequiredEnv)
	}

	const count = 4
	enc, err := NewEncoder(rdW, rdH, 30)
	if err != nil {
		t.Fatal(err)
	}
	enc.SetKeyFrameInterval(count)

	var frames [][]byte
	var encRecon []refFrameBuffer
	for i := 0; i < count; i++ {
		b, err := enc.Encode(rdSource(i))
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		frames = append(frames, b)
		last := enc.refFrames.last
		encRecon = append(encRecon, refFrameBuffer{
			Y:  append([]byte(nil), last.Y...),
			Cb: append([]byte(nil), last.Cb...),
			Cr: append([]byte(nil), last.Cr...),
		})
	}

	data, err := ivf.Marshal(ivf.Config{
		Width: rdW, Height: rdH, FPSNumerator: 30, FPSDenominator: 1,
	}, frames)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := vp8check.Decode(tool, data, rdW, rdH)
	if err != nil {
		t.Fatalf("the reference decoder rejected the encoder's output: %v", err)
	}
	if len(decoded) != count {
		t.Fatalf("decoded %d frames, want %d", len(decoded), count)
	}

	for i := 0; i < count; i++ {
		isKey := frames[i][0]&1 == 0
		planes := []struct {
			name     string
			enc, dec []byte
		}{
			{"Y", encRecon[i].Y, decoded[i].Y},
			{"U", encRecon[i].Cb, decoded[i].U},
			{"V", encRecon[i].Cr, decoded[i].V},
		}
		for _, p := range planes {
			differing, maxAbs := rdDiff(p.enc, p.dec)
			kind := "inter"
			if isKey {
				kind = "key"
			}
			t.Logf("frame %d (%-5s) plane %s: %d/%d bytes differ, max |delta| = %d",
				i, kind, p.name, differing, len(p.enc), maxAbs)
			if differing != 0 {
				t.Errorf("frame %d (%s) plane %s: the encoder's reference disagrees with the decoder "+
					"on %d of %d bytes (max |delta| %d) — the prediction loop is open, and the error "+
					"will compound into every frame that follows",
					i, kind, p.name, differing, len(p.enc), maxAbs)
			}
		}
	}
}

// TestDirtyMapKeepsReconstructionExact checks the fast path the screen encoder
// relies on: macroblocks reported as unchanged are coded as skipped ZEROMV
// references without any analysis, and the result must still match libvpx byte
// for byte.
//
// The risk this guards against is specific. A skipped macroblock reconstructs
// to whatever the reference holds, so if the encoder's reference and the
// decoder's ever part company, a skip freezes the difference in place instead
// of coding over it — and it stays frozen until the next key frame.
func TestDirtyMapKeepsReconstructionExact(t *testing.T) {
	tool, required, err := vp8check.Availability()
	if err != nil {
		if required {
			t.Fatalf("%s is required but unavailable: %v", vp8check.ToolName, err)
		}
		t.Skipf("skipping: %v", err)
	}

	const count = 4
	enc, err := NewEncoder(rdW, rdH, 30)
	if err != nil {
		t.Fatal(err)
	}
	enc.SetKeyFrameInterval(count)

	mbW, mbH := rdW/16, rdH/16
	var frames [][]byte
	var encRecon [][]byte
	for i := 0; i < count; i++ {
		if i > 0 {
			// Only the leftmost column of macroblocks is reported as changed.
			// The source changes there and nowhere else, so this is the truth
			// rather than a convenient lie.
			dirty := make([]bool, mbW*mbH)
			for y := 0; y < mbH; y++ {
				dirty[y*mbW] = true
			}
			enc.SetDirtyMacroblocks(dirty)
		}
		b, err := enc.Encode(dirtySource(i))
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		frames = append(frames, b)
		encRecon = append(encRecon, append([]byte(nil), enc.refFrames.last.Y...))
	}

	data, err := ivf.Marshal(ivf.Config{
		Width: rdW, Height: rdH, FPSNumerator: 30, FPSDenominator: 1,
	}, frames)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := vp8check.Decode(tool, data, rdW, rdH)
	if err != nil {
		t.Fatalf("the reference decoder rejected the output: %v", err)
	}

	for i := range decoded {
		differing, maxAbs := rdDiff(encRecon[i], decoded[i].Y)
		if differing != 0 {
			t.Errorf("frame %d: %d/%d luma bytes differ (max |delta| %d) with the dirty map in use",
				i, differing, len(encRecon[i]), maxAbs)
		}
	}
}

// dirtySource changes only the leftmost 16 pixels between frames.
func dirtySource(idx int) []byte {
	buf := make([]byte, 0, rdW*rdH*3/2)
	for y := 0; y < rdH; y++ {
		for x := 0; x < rdW; x++ {
			v := (x/8+y/8)%2*90 + 60
			if x < 16 {
				v = 40 + idx*30
			}
			buf = append(buf, byte(v))
		}
	}
	for i := 0; i < rdW*rdH/2; i++ {
		buf = append(buf, 128)
	}
	return buf
}
