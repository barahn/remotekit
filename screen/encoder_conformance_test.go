package screen_test

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/screen/codec/conformance"
	"github.com/barahn/remotekit/screen/codec/ivf"
)

const (
	testWidth  = 64
	testHeight = 64
)

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

func testFrame(t *testing.T) *screen.Frame {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, testWidth, testHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 40, G: 90, B: 160, A: 255}}, image.Point{}, draw.Src)
	return &screen.Frame{
		Image:        img,
		Bounds:       img.Bounds(),
		CapturedAt:   time.Now(),
		SequenceNum:  1,
		DisplayIndex: 0,
	}
}

// TestPlaceholderEncoderIsNotConformant holds the KNOWN LIMITATION documented on
// screen.VP8Encoder as an executable fact: its output is a VP8 key frame header
// glued to a JPEG payload, and no VP8 decoder can read it.
//
// This test asserts the current, broken behaviour on purpose. It exists so that
// the oracle is already wired to the production encoder path when the real
// encoder lands, and so the placeholder cannot be quietly shipped. DELETE IT and
// replace it with a positive conformance assertion as part of that change — see
// docs/reports/chirp-separation-plan.md, Track 3.
func TestPlaceholderEncoderIsNotConformant(t *testing.T) {
	tool := requireTool(t)

	enc := screen.NewVP8Encoder(30, 70)
	sample, err := enc.Encode(testFrame(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(sample) == 0 {
		t.Fatal("Encode returned no sample")
	}

	data, err := ivf.Marshal(ivf.Config{
		Width: testWidth, Height: testHeight,
		FPSNumerator: 30, FPSDenominator: 1,
	}, [][]byte{sample})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	frames, err := conformance.Decode(tool, data, testWidth, testHeight)
	if err == nil {
		t.Fatalf("the reference decoder accepted %d frames from the placeholder encoder; "+
			"if the real encoder has landed, replace this test with a positive conformance assertion",
			len(frames))
	}
	t.Logf("placeholder rejected as expected: %v", err)
}
