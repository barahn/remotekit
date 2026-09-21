// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// TestRealCaptureConformance encodes frames captured from the actual display
// and checks that a reference decoder accepts and reconstructs them.
//
// Every conformance test so far has used synthetic sources chosen by hand.
// A real desktop is the only content that settles whether this encoder is fit
// for what it was built for, and it is content nobody designed to be easy.
//
// It captures the machine's screen, so it runs only when asked:
// VP8_REAL_CAPTURE=1. Set VP8_REAL_CAPTURE_DIR to keep the stream and its
// sources for decoding elsewhere — the reference decoder usually lives in a
// container and the screen does not. Nothing about the captured picture is
// logged: only frame sizes, PSNR and byte counts.
//
// Measured on this machine's 1920x1080 X11 desktop on 2026-09-16, eight frames:
// libvpx accepts the stream and reconstructs every frame at 18.92 dB, with
// 2007995 of 2073600 luma bytes differing from the source. The encoder's own
// reconstruction is far better, so the two disagree; the mode layer meanwhile
// stays in sync for every macroblock and the frames parse cleanly. That makes
// it a residual-layer defect, distinct from the mode-context one fixed
// alongside this.
//
// An earlier run of this test reported 3840x2160 and correspondingly worse
// numbers. The display is FullHD, so those figures described something this
// machine does not have; they are not repeatable and should not be quoted. The
// conclusion survives the correction — the defect reproduces at the real
// resolution — but the magnitudes in that first report do not.
func TestRealCaptureConformance(t *testing.T) {
	if os.Getenv("VP8_REAL_CAPTURE") == "" {
		t.Skip("set VP8_REAL_CAPTURE=1 to capture this machine's screen")
	}
	outDir := os.Getenv("VP8_REAL_CAPTURE_DIR")
	if outDir == "" {
		outDir = t.TempDir()
	}
	tool, _, toolErr := vp8check.Availability()

	cap, err := NewCapturer(DefaultConfig())
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cap.Start(ctx); err != nil {
		t.Fatalf("capture did not start: %v", err)
	}
	defer cap.Stop()

	enc := NewVP8Encoder(30, 70)

	const want = 8
	var samples [][]byte
	var sources [][]byte
	var width, height int

	deadline := time.After(15 * time.Second)
	for len(samples) < want {
		select {
		case frame, ok := <-cap.Frames():
			if !ok {
				t.Fatalf("the capture channel closed after %d frames", len(samples))
			}
			if frame == nil || frame.Image == nil {
				continue
			}
			w, h := codedSize(frame.Image.Bounds())
			if width == 0 {
				width, height = w, h
				t.Logf("capturing %dx%d", width, height)
			}
			if w != width || h != height {
				continue // a resolution change mid-test would confuse the comparison
			}

			sample, encErr := enc.Encode(frame)
			if encErr != nil {
				t.Fatalf("Encode: %v", encErr)
			}
			if len(sample) == 0 {
				continue // nothing changed on screen; nothing to send
			}
			yuv := make([]byte, width*height*3/2)
			rgbaToI420(yuv, frame.Image, width, height)
			samples = append(samples, sample)
			sources = append(sources, yuv)
		case <-deadline:
			if len(samples) == 0 {
				t.Fatal("no frames captured in 15s")
			}
			t.Logf("stopping early with %d frames", len(samples))
			goto done
		}
	}
done:

	keys := 0
	total := 0
	for _, s := range samples {
		if s[0]&1 == 0 {
			keys++
		}
		total += len(s)
	}
	t.Logf("%d frames, %d key, %d bytes total, %d bytes average",
		len(samples), keys, total, total/len(samples))

	data, err := ivf.Marshal(ivf.Config{
		Width: uint16(width), Height: uint16(height), FPSNumerator: 30, FPSDenominator: 1,
	}, samples)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outDir+"/real.ivf", data, 0o600); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for _, src := range sources {
		raw = append(raw, src...)
	}
	if err := os.WriteFile(outDir+"/real_src.yuv", raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if toolErr != nil {
		// The decoder usually lives in a container and the screen does not, so
		// the stream and its sources are left on disk to be checked there.
		t.Skipf("captured %d frames of %dx%d to %s; %s is not on this host, so decode them elsewhere",
			len(samples), width, height, outDir, vp8check.ToolName)
	}

	decoded, err := vp8check.Decode(tool, data, width, height)
	if err != nil {
		t.Fatalf("the reference decoder rejected frames captured from a real screen: %v", err)
	}
	if len(decoded) != len(samples) {
		t.Fatalf("decoded %d of %d frames", len(decoded), len(samples))
	}

	worst := 999.0
	for i := range decoded {
		psnr, pErr := vp8check.PSNR(decoded[i].Y, sources[i][:width*height])
		if pErr != nil {
			t.Fatal(pErr)
		}
		kind := "inter"
		if samples[i][0]&1 == 0 {
			kind = "key"
		}
		t.Logf("frame %d (%-5s) %d bytes, luma PSNR %.2f dB", i, kind, len(samples[i]), psnr)
		if psnr < worst {
			worst = psnr
		}
	}
	if worst < 30 {
		t.Errorf("worst frame reconstructed at %.2f dB, want at least 30", worst)
	}
}
