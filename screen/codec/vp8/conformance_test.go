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
