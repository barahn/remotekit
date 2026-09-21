// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
// Forked from github.com/opd-ai/vp8 (MIT); see LICENSE.upstream.

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

// TestInterFramesAreConformant checks the half of the encoder that was broken
// when this fork landed.
//
// Upstream's inter frames decoded to between 8 and 13 dB — unrecognisable, and
// degrading across the sequence as the drift compounded. The fault was in the
// first partition: the mode and motion-vector layer was coding its own
// probabilities and its own tree shapes instead of the format's, so the decoder
// read something other than what the encoder wrote. With that layer rewritten
// against RFC 6386 §16–18, inter frames now reconstruct above the key frame
// they follow, which is the ordinary result — an inter frame codes a smaller
// residual at the same quantiser.
func TestInterFramesAreConformant(t *testing.T) {
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

	for i := 0; i < len(decoded); i++ {
		psnr, err := vp8check.PSNR(decoded[i].Y, sourceFrame(i)[:confWidth*confHeight])
		if err != nil {
			t.Fatalf("frame %d: PSNR: %v", i, err)
		}
		kind := "inter"
		if i == 0 {
			kind = "key"
		}
		t.Logf("%s frame %d: luma PSNR %.2f dB", kind, i, psnr)
		if psnr < 30 {
			t.Errorf("frame %d reconstructed at %.2f dB, want at least 30", i, psnr)
		}
	}
}

// TestScreenContentProfileKeepsConformance checks that restricting inter
// macroblocks to ZEROMV does not cost correctness, and says what it does buy.
//
// The profile was built on a theory that turned out to be wrong — that the
// inherited corruption lived in MV_NEW — and it did not fix anything. What it
// is actually for survives that: for screen content the motion search finds
// nothing worth coding, so skipping it produces smaller frames and costs no
// quality. Both configurations must decode faithfully.
func TestScreenContentProfileKeepsConformance(t *testing.T) {
	tool := requireTool(t)

	const count = 6
	sizes := map[bool]int{}

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
			sizes[screen] += len(b)
		}
		for i, got := range decodeWithOracle(t, tool, frames) {
			psnr, err := vp8check.PSNR(got.Y, sourceFrame(i)[:confWidth*confHeight])
			if err != nil {
				t.Fatalf("PSNR: %v", err)
			}
			if psnr < 30 {
				t.Errorf("screenContent=%v: frame %d at %.2f dB, want at least 30", screen, i, psnr)
			}
		}
	}
	t.Logf("total bytes: without the profile %d, with it %d", sizes[false], sizes[true])
}

// TestUnchangingSourceReconstructsFaithfully is the sharpest form of the
// inter-frame check: with nothing moving, every inter macroblock is a ZEROMV
// copy, so the decoder must reproduce the key frame it already has.
//
// This was a bisection diagnostic while the inter path was broken — it measured
// 13 dB, which is how far the decoder was from reproducing its own reference.
func TestUnchangingSourceReconstructsFaithfully(t *testing.T) {
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
	if len(decoded) != count {
		t.Fatalf("decoded %d frames, want %d", len(decoded), count)
	}
	for i := 1; i < count; i++ {
		psnr, err := vp8check.PSNR(decoded[i].Y, decoded[0].Y)
		if err != nil {
			t.Fatalf("PSNR: %v", err)
		}
		t.Logf("inter frame %d vs the decoded key frame: %.2f dB", i, psnr)
		if psnr < 35 {
			t.Errorf("inter frame %d is %.2f dB from the key frame it should be reproducing", i, psnr)
		}
	}
}
