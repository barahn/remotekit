// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import (
	"fmt"
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// TestConformanceAcrossQuantisers decodes streams from across VP8's whole
// quantiser range.
//
// Everything decoded through libvpx so far used one quantiser near the fine
// end. The coarse half of the range — index 64 to 127 — was unreachable until
// rate control needed it, and unreachable code is untested code.
func TestConformanceAcrossQuantisers(t *testing.T) {
	tool, required, err := vp8check.Availability()
	if err != nil {
		if required {
			t.Fatalf("%s required but unavailable: %v", vp8check.ToolName, err)
		}
		t.Skipf("skipping: %v", err)
	}

	const w, h = 320, 240
	qis := []int{0, 4, 24, 63, 64, 96, 127}
	if testing.Short() {
		// The ends and the start of the coarse half; CI runs the full sweep
		// in a step without -race.
		qis = []int{0, 64, 127}
	}
	for _, qi := range qis {
		enc, err := NewEncoder(w, h, 30)
		if err != nil {
			t.Fatal(err)
		}
		enc.SetKeyFrameInterval(4)
		enc.SetScreenContentProfile(true)
		enc.qi = qi

		const count = 4
		var frames [][]byte
		var recon [][]byte
		for i := 0; i < count; i++ {
			b, err := enc.Encode(textLikeSource(w, h, i))
			if err != nil {
				t.Fatalf("qi=%d: %v", qi, err)
			}
			frames = append(frames, b)
			recon = append(recon, append([]byte(nil), enc.refFrames.last.Y...))
		}
		data, err := ivf.Marshal(ivf.Config{
			Width: w, Height: h, FPSNumerator: 30, FPSDenominator: 1,
		}, frames)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := vp8check.Decode(tool, data, w, h)
		if err != nil {
			t.Errorf("qi=%d: the reference decoder rejected the stream: %v", qi, err)
			continue
		}
		worstPSNR := 999.0
		totalDiff := 0
		for i := range decoded {
			differing, _ := rdDiff(recon[i], decoded[i].Y)
			totalDiff += differing
			p, _ := vp8check.PSNR(decoded[i].Y, textLikeSource(w, h, i)[:w*h])
			if p < worstPSNR {
				worstPSNR = p
			}
		}
		total := 0
		for _, f := range frames {
			total += len(f)
		}
		fmt.Printf("qi=%-3d  %5d bytes  pior PSNR %5.2f dB  bytes divergentes: %d\n",
			qi, total, worstPSNR, totalDiff)
		if totalDiff != 0 {
			t.Errorf("qi=%d: the encoder's reference disagrees with the decoder on %d bytes", qi, totalDiff)
		}
	}
}
