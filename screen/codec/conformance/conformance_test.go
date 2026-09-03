package conformance_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/barahn/remotekit/screen/codec/conformance"
	"github.com/barahn/remotekit/screen/codec/ivf"
)

// Dimensions and frame count of testdata/keyframes.ivf.
const (
	fixtureWidth  = 64
	fixtureHeight = 64
	fixtureFrames = 3
)

// requireTool resolves the reference decoder, skipping when it is absent unless
// the run demands it. See conformance.RequiredEnv.
func requireTool(t *testing.T) string {
	t.Helper()
	path, required, err := conformance.Availability()
	if err == nil {
		return path
	}
	if required {
		t.Fatalf("%s is required (%s is set) but unavailable: %v\n"+
			"Install it with: apt-get install vpx-tools", conformance.ToolName, conformance.RequiredEnv, err)
	}
	t.Skipf("skipping conformance check: %v (set %s=1 to make this fatal)", err, conformance.RequiredEnv)
	return ""
}

// referenceFrame reproduces the source pattern that testdata/keyframes.ivf was
// encoded from: a luma ramp that varies with position and frame index, over
// neutral chroma. Keeping the generator here rather than committing the raw
// YUV keeps the fixture small and makes the expected content readable.
func referenceFrame(frameIdx, width, height int) []byte {
	out := make([]byte, 0, width*height*3/2)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			out = append(out, byte((x*3+y*5+frameIdx*17)&0xff))
		}
	}
	for i := 0; i < width*height/2; i++ {
		out = append(out, 128)
	}
	return out
}

// TestOracleAcceptsKnownGoodStream is the positive control. An oracle that only
// ever rejects proves nothing, so this asserts that a stream produced by
// libvpx's own encoder decodes cleanly and reconstructs the source.
func TestOracleAcceptsKnownGoodStream(t *testing.T) {
	tool := requireTool(t)

	data, err := os.ReadFile(filepath.Join("testdata", "keyframes.ivf"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	frames, err := conformance.Decode(tool, data, fixtureWidth, fixtureHeight)
	if err != nil {
		t.Fatalf("reference decoder rejected a known-good stream: %v", err)
	}
	if len(frames) != fixtureFrames {
		t.Fatalf("decoded %d frames, want %d", len(frames), fixtureFrames)
	}

	for i, got := range frames {
		want := referenceFrame(i, fixtureWidth, fixtureHeight)
		psnr, err := conformance.PSNR(got.Y, want[:fixtureWidth*fixtureHeight])
		if err != nil {
			t.Fatalf("frame %d: PSNR: %v", i, err)
		}
		// The fixture was encoded at a near-lossless quantiser, so anything
		// below this means the harness is misreading the decoder's output
		// rather than that the encoder did a poor job.
		if psnr < 30 {
			t.Errorf("frame %d: luma PSNR %.2f dB, want >= 30 dB", i, psnr)
		}
		t.Logf("frame %d: luma PSNR %.2f dB", i, psnr)
	}
}

// TestOracleRejectsCorruptStream is the negative control: the oracle must
// actually detect a broken bitstream, not merely tolerate whatever it is given.
func TestOracleRejectsCorruptStream(t *testing.T) {
	tool := requireTool(t)

	garbage := make([]byte, 512)
	for i := range garbage {
		garbage[i] = byte(i * 7)
	}
	data, err := ivf.Marshal(ivf.Config{
		Width: fixtureWidth, Height: fixtureHeight,
		FPSNumerator: 30, FPSDenominator: 1,
	}, [][]byte{garbage})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := conformance.Decode(tool, data, fixtureWidth, fixtureHeight); err == nil {
		t.Fatal("reference decoder accepted a corrupt stream; the oracle has no teeth")
	} else {
		var decErr *conformance.DecodeError
		if !errors.As(err, &decErr) {
			t.Fatalf("got %T (%v), want *conformance.DecodeError", err, err)
		}
		t.Logf("rejected as expected: %v", decErr)
	}
}

func TestPSNR(t *testing.T) {
	t.Parallel()

	identical := []byte{1, 2, 3, 4}
	got, err := conformance.PSNR(identical, identical)
	if err != nil {
		t.Fatalf("identical planes: %v", err)
	}
	if !math.IsInf(got, 1) {
		t.Errorf("identical planes: got %v, want +Inf", got)
	}

	if _, err := conformance.PSNR([]byte{1}, []byte{1, 2}); err == nil {
		t.Error("mismatched lengths: expected an error")
	}
	if _, err := conformance.PSNR(nil, nil); err == nil {
		t.Error("empty planes: expected an error")
	}

	// A uniform off-by-one across the plane is 20*log10(255) dB.
	a := []byte{10, 10, 10, 10}
	b := []byte{11, 11, 11, 11}
	got, err = conformance.PSNR(a, b)
	if err != nil {
		t.Fatalf("off-by-one: %v", err)
	}
	if want := 20 * math.Log10(255); math.Abs(got-want) > 1e-9 {
		t.Errorf("off-by-one: got %v, want %v", got, want)
	}
}
