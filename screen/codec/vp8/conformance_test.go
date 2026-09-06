package vp8_test

import (
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

const (
	confWidth  = 64
	confHeight = 64
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

// sourceFrame is a moving pattern: static enough that inter prediction has
// something to work with, moving enough that it cannot all be skipped.
func sourceFrame(idx int) []byte {
	buf := make([]byte, 0, confWidth*confHeight*3/2)
	for y := 0; y < confHeight; y++ {
		for x := 0; x < confWidth; x++ {
			v := (x/8+y/8)%2*90 + 60
			if x >= idx*3 && x < idx*3+8 {
				v = 230 // a bar that walks right, one macroblock wide
			}
			buf = append(buf, byte(v))
		}
	}
	for i := 0; i < confWidth*confHeight/2; i++ {
		buf = append(buf, 128)
	}
	return buf
}

// encodeSequence returns the raw VP8 frames the forked encoder produces.
func encodeSequence(t *testing.T, frames, keyInterval int) [][]byte {
	t.Helper()
	enc, err := vp8.NewEncoder(confWidth, confHeight, 30)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	enc.SetKeyFrameInterval(keyInterval)
	out := make([][]byte, 0, frames)
	for i := 0; i < frames; i++ {
		b, err := enc.Encode(sourceFrame(i))
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		out = append(out, b)
	}
	return out
}

func decodeWithOracle(t *testing.T, tool string, frames [][]byte) []vp8check.Frame {
	t.Helper()
	data, err := ivf.Marshal(ivf.Config{
		Width: confWidth, Height: confHeight,
		FPSNumerator: 30, FPSDenominator: 1,
	}, frames)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := vp8check.Decode(tool, data, confWidth, confHeight)
	if err != nil {
		t.Fatalf("the reference decoder rejected the encoder's output: %v", err)
	}
	return decoded
}

// TestKeyFramesAreConformant checks the half of the encoder upstream did
// validate — but against libvpx rather than golang.org/x/image/vp8, which is
// the same decoder upstream used and therefore not an independent check.
func TestKeyFramesAreConformant(t *testing.T) {
	tool := requireTool(t)

	const count = 3
	decoded := decodeWithOracle(t, tool, encodeSequence(t, count, 0)) // 0 = every frame is a key frame
	if len(decoded) != count {
		t.Fatalf("decoded %d frames, want %d", len(decoded), count)
	}
	for i, got := range decoded {
		want := sourceFrame(i)[:confWidth*confHeight]
		psnr, err := vp8check.PSNR(got.Y, want)
		if err != nil {
			t.Fatalf("frame %d: PSNR: %v", i, err)
		}
		t.Logf("key frame %d: luma PSNR %.2f dB", i, psnr)
		if psnr < 25 {
			t.Errorf("key frame %d: luma PSNR %.2f dB is too low to be a faithful encode", i, psnr)
		}
	}
}

// TestInterFramesAreBrokenUpstream pins the defect this fork inherited.
//
// Upstream's GAPS.md §2 records that inter-frame output is asserted only on the
// frame-type bit and a minimum byte length — no decoder ever parsed it — and §1
// records that MV prediction may diverge from RFC 6386 §18.2. Run through
// libvpx, the reconstruction is not slightly off; it is unrecognisable, and it
// degrades across the sequence, which is what compounding drift looks like:
//
//	key   frame 0: 39.28 dB
//	inter frame 1: 11.55 dB
//	inter frame 2: 11.20 dB
//	inter frame 3:  7.84 dB
//
// This test asserts the broken state on purpose, so the defect cannot be
// forgotten and so a fix is detectable. DELETE IT and restore a positive
// assertion when the restricted profile lands — MV_ZERO + skip + intra never
// enters MV_NEW, where both defects live, so that profile is expected to pass
// where this does not.
func TestInterFramesAreBrokenUpstream(t *testing.T) {
	tool := requireTool(t)

	const count = 6
	frames := encodeSequence(t, count, count) // one key frame, then inter frames
	if frames[0][0]&1 != 0 {
		t.Fatal("the first frame is not a key frame")
	}
	inter := 0
	for _, f := range frames[1:] {
		if f[0]&1 == 1 {
			inter++
		}
	}
	if inter == 0 {
		t.Fatal("no inter frames were produced; this test would prove nothing")
	}

	decoded := decodeWithOracle(t, tool, frames)
	if len(decoded) != count {
		t.Fatalf("decoded %d frames, want %d", len(decoded), count)
	}

	// The key frame is sound; everything after it is not.
	keyPSNR, err := vp8check.PSNR(decoded[0].Y, sourceFrame(0)[:confWidth*confHeight])
	if err != nil {
		t.Fatalf("key frame PSNR: %v", err)
	}
	if keyPSNR < 25 {
		t.Errorf("key frame luma PSNR %.2f dB; the key-frame path is supposed to be the trustworthy half", keyPSNR)
	}
	t.Logf("key frame: luma PSNR %.2f dB", keyPSNR)

	for i := 1; i < len(decoded); i++ {
		psnr, err := vp8check.PSNR(decoded[i].Y, sourceFrame(i)[:confWidth*confHeight])
		if err != nil {
			t.Fatalf("inter frame %d: PSNR: %v", i, err)
		}
		t.Logf("inter frame %d: luma PSNR %.2f dB", i, psnr)
		if psnr >= 25 {
			t.Fatalf("inter frame %d reconstructed faithfully at %.2f dB — the inherited defect appears fixed, "+
				"so replace this test with a positive conformance assertion", i, psnr)
		}
	}
}

// TestScreenContentProfileDoesNotFixInterFrames records a negative result.
//
// The working theory when the fork landed was that the inherited inter-frame
// corruption lived in MV_NEW — upstream's GAPS.md §1 documents an MV predictor
// that may diverge from RFC 6386 §18.2, and MV_NEW codes only a delta against
// the predictor the decoder derives. Restricting inter macroblocks to ZEROMV
// should therefore have designed the defect out.
//
// It does not. With SetScreenContentProfile enabled the reconstruction is
// unchanged, to the byte: same frame sizes, same PSNR. Those macroblocks were
// already choosing ZEROMV, so the fault lies elsewhere.
//
// The theory was wrong, and this test exists so it is not quietly retried.
func TestScreenContentProfileDoesNotFixInterFrames(t *testing.T) {
	tool := requireTool(t)

	const count = 6
	sizes := map[bool][]int{}
	psnrs := map[bool][]float64{}

	for _, screen := range []bool{false, true} {
		enc, err := vp8.NewEncoder(confWidth, confHeight, 30)
		if err != nil {
			t.Fatalf("NewEncoder: %v", err)
		}
		enc.SetKeyFrameInterval(count)
		enc.SetScreenContentProfile(screen)

		frames := make([][]byte, 0, count)
		for i := 0; i < count; i++ {
			b, err := enc.Encode(sourceFrame(i))
			if err != nil {
				t.Fatalf("Encode frame %d: %v", i, err)
			}
			frames = append(frames, b)
			sizes[screen] = append(sizes[screen], len(b))
		}
		for i, got := range decodeWithOracle(t, tool, frames) {
			psnr, err := vp8check.PSNR(got.Y, sourceFrame(i)[:confWidth*confHeight])
			if err != nil {
				t.Fatalf("PSNR: %v", err)
			}
			psnrs[screen] = append(psnrs[screen], psnr)
		}
		t.Logf("screenContent=%-5v sizes=%v", screen, sizes[screen])
	}

	// The key frame is sound either way; the inter frames are not, either way.
	for _, screen := range []bool{false, true} {
		if psnrs[screen][0] < 25 {
			t.Errorf("screenContent=%v: key frame %.2f dB, expected the key-frame path to be sound", screen, psnrs[screen][0])
		}
		for i := 1; i < count; i++ {
			if psnrs[screen][i] >= 25 {
				t.Fatalf("screenContent=%v: inter frame %d reconstructed at %.2f dB — the defect appears fixed, "+
					"so this negative result is stale and the test should be replaced", screen, i, psnrs[screen][i])
			}
		}
	}
	t.Logf("inter PSNR without the profile: %.2f %.2f %.2f", psnrs[false][1], psnrs[false][2], psnrs[false][3])
	t.Logf("inter PSNR with the profile:    %.2f %.2f %.2f", psnrs[true][1], psnrs[true][2], psnrs[true][3])
}

// TestBisectInterFaultWithIdenticalFrames narrows where the fault is not.
//
// Feeding the encoder the same frame repeatedly means every inter macroblock
// has nothing to code. If reconstruction still drifts under those conditions,
// the fault is not in motion vectors, mode decision or residual coding — there
// are none of consequence. Measured: the first inter frame lands 13 dB away
// from the key frame the decoder just produced, so the decoder is not
// reproducing the reference at all.
func TestBisectInterFaultWithIdenticalFrames(t *testing.T) {
	tool := requireTool(t)
	const count = 4
	src := sourceFrame(0)

	enc, err := vp8.NewEncoder(confWidth, confHeight, 30)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	enc.SetKeyFrameInterval(count)
	enc.SetScreenContentProfile(true)

	frames := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		b, err := enc.Encode(src)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		frames = append(frames, b)
	}
	decoded := decodeWithOracle(t, tool, frames)

	keyVsSource, _ := vp8check.PSNR(decoded[0].Y, src[:confWidth*confHeight])
	if keyVsSource < 25 {
		t.Errorf("key frame %.2f dB against an unchanging source", keyVsSource)
	}
	firstInterVsKey, err := vp8check.PSNR(decoded[1].Y, decoded[0].Y)
	if err != nil {
		t.Fatalf("PSNR: %v", err)
	}
	t.Logf("key frame vs source: %.2f dB | first inter frame vs decoded key frame: %.2f dB", keyVsSource, firstInterVsKey)
	if firstInterVsKey >= 25 {
		t.Fatalf("the first inter frame now reproduces the reference at %.2f dB — this diagnostic is stale", firstInterVsKey)
	}
}
