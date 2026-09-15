package vp8

import (
	"bytes"
	"testing"
)

// TestParallelAnalysisIsBitExact is the assertion that makes the parallel
// key-frame analysis pass safe to have at all.
//
// Running the mode search across goroutines is only legitimate if the result
// cannot depend on the order rows finish in. Rather than argue that from the
// code, this encodes the same frames both ways and compares the bitstreams byte
// for byte. A missed dependency would show up here as a difference that comes
// and goes between runs, so the comparison covers several frames and both frame
// types.
func TestParallelAnalysisIsBitExact(t *testing.T) {
	const w, h = 320, 192 // 240 macroblocks: enough rows to spread across workers

	encode := func(threshold int) [][]byte {
		saved := parallelRowThreshold
		parallelRowThreshold = threshold
		defer func() { parallelRowThreshold = saved }()

		enc, err := NewEncoder(w, h, 30)
		if err != nil {
			t.Fatal(err)
		}
		enc.SetKeyFrameInterval(3) // key, inter, inter, key, ...
		var out [][]byte
		for i := 0; i < 6; i++ {
			b, err := enc.Encode(benchFrame(w, h, i))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		return out
	}

	serial := encode(1 << 30) // threshold above any frame: never parallel
	parallel := encode(1)     // threshold below any frame: always parallel

	if len(serial) != len(parallel) {
		t.Fatalf("frame counts differ: %d serial, %d parallel", len(serial), len(parallel))
	}
	for i := range serial {
		if !bytes.Equal(serial[i], parallel[i]) {
			t.Errorf("frame %d differs: %d bytes serial, %d bytes parallel — the analysis pass "+
				"depends on row completion order, which means it is not safe to parallelise",
				i, len(serial[i]), len(parallel[i]))
		}
	}
}

// benchFrame draws text-like screen content with a region that moves between
// frames, so both the key-frame and inter-frame paths get real work.
func benchFrame(w, h, idx int) []byte {
	buf := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 240
			if (x/12+y/16)%3 == 0 && x%12 < 6 && y%16 < 7 {
				v = 30
			}
			if x >= idx*4 && x < idx*4+24 && y < 32 {
				v = 120
			}
			buf[y*w+x] = byte(v)
		}
	}
	for i := w * h; i < len(buf); i++ {
		buf[i] = byte(100 + (i % 40))
	}
	return buf
}
