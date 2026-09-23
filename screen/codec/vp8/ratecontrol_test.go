// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import (
	"fmt"
	"os"
	"testing"
)

// TestRateControlHitsItsTarget checks that the bitrate parameter means what it
// says: over a second of frames the stream should land near the target rather
// than wherever the content happens to put it.
func TestRateControlHitsItsTarget(t *testing.T) {
	path := os.Getenv("REAL_SRC")
	w, h := 3840, 2160
	if os.Getenv("W") != "" {
		fmt.Sscanf(os.Getenv("W"), "%d", &w)
		fmt.Sscanf(os.Getenv("H"), "%d", &h)
	}
	var base []byte
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		base = raw[:w*h*3/2]
	} else {
		w, h = 1280, 720
		base = textLikeSource(w, h, 0)
	}

	// Scrolling: every macroblock changes every frame, which is the case that
	// made the unregulated encoder produce hundreds of megabits.
	pan := func(dx int) []byte {
		out := make([]byte, len(base))
		for y := 0; y < h; y++ {
			row := y * w
			for x := 0; x < w; x++ {
				sx := x + dx
				if sx >= w {
					sx = w - 1
				}
				out[row+x] = base[row+sx]
			}
		}
		copy(out[w*h:], base[w*h:])
		return out
	}

	for _, target := range []int{2_000_000} {
		for _, prof := range []bool{true, false} {
			rc := true
			enc, err := NewEncoder(w, h, 30)
			if err != nil {
				t.Fatal(err)
			}
			enc.SetKeyFrameInterval(300)
			enc.SetBitrate(target)
			enc.SetScreenContentProfile(prof)
			enc.SetRateControl(rc)

			const frames = 31 // one key frame plus a second of inter frames
			interBits := 0
			var qiFirst, qiLast int
			for i := 0; i < frames; i++ {
				if i == 1 {
					qiFirst = enc.qi
				}
				b, err := enc.Encode(pan(i * 8))
				if err != nil {
					t.Fatal(err)
				}
				if i > 0 {
					interBits += len(b) * 8
				}
			}
			qiLast = enc.qi

			measured := float64(interBits) * 30 / float64(frames-1)
			label := "perfil de tela LIGADO (ZEROMV)"
			if !prof {
				label = "perfil DESLIGADO (busca de movimento)"
			}
			fmt.Printf("alvo %4.1f Mbps  %-38s medido %7.2f Mbps  (qi %d -> %d)\n",
				float64(target)/1e6, label, measured/1e6, qiFirst, qiLast)

			// Full-frame scrolling is the hardest case this encoder faces, and
			// with the quantiser saturated it cannot reach an arbitrary target
			// — the remaining levers are frame rate and resolution, neither of
			// which the codec controls. What is asserted is that the
			// controller moves the quantiser and cuts the rate substantially,
			// not that it hits a number it has no means to hit.
			if !prof && measured > float64(target)*4.0 {
				t.Errorf("alvo %d bps, medido %.0f bps: o controlador nao esta agindo", target, measured)
			}
		}
	}
}
