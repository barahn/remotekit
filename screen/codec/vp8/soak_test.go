// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
	"github.com/barahn/remotekit/screen/codec/vp8"
	"github.com/barahn/remotekit/screen/codec/vp8check"
)

// The soak test runs the encoder for long enough that a defect which only
// shows up over time has somewhere to show up: reference drift that compounds
// across key-frame cycles, a leak in the per-frame buffers, or a wavefront
// worker that is not reaped. The short conformance tests each cover a handful
// of frames, which is far too few to see any of that.
const (
	soakWidth  = 640
	soakHeight = 480
	soakFPS    = 30

	// One key frame every two seconds.
	soakKeyInterval = 60

	// The oracle is handed the stream in chunks so the decoded pictures do not
	// have to be held all at once. A chunk must begin on a key frame to be
	// independently decodable, so its length is a multiple of the interval.
	soakChunk = 240

	// A bitrate the content comfortably fits into, so an overshoot means the
	// controller is failing rather than the content being too hard.
	soakBitrate = 2_000_000
)

// soakFrames is the length of the run: 2880 frames, 96 seconds of video, 48
// key-frame cycles.
const soakFrames = 2880

// soakFrame is a moving pattern with structured chroma. Flat chroma is a trap:
// it lets a luma-only encode look correct, which is how an early reading of
// this encoder concluded the inter-frame fault was confined to luma.
func soakFrame(idx int) []byte {
	buf := make([]byte, 0, soakWidth*soakHeight*3/2)
	for y := 0; y < soakHeight; y++ {
		for x := 0; x < soakWidth; x++ {
			v := (x/16+y/16)%2*70 + 70
			// A bar that sweeps right and wraps, one macroblock wide.
			if bar := (idx * 7) % soakWidth; x >= bar && x < bar+16 {
				v = 235
			}
			// A slow global fade, so no frame is a repeat of an earlier one.
			v += (idx / 30) % 11
			buf = append(buf, byte(v))
		}
	}
	for _, base := range []int{90, 160} {
		for y := 0; y < soakHeight/2; y++ {
			for x := 0; x < soakWidth/2; x++ {
				c := base + (x/8+y/8+idx)%2*40
				buf = append(buf, byte(c))
			}
		}
	}
	return buf
}

func isKeyFrame(f []byte) bool { return len(f) > 0 && f[0]&1 == 0 }

// decodeSoakChunk hands one self-contained chunk to the reference decoder and
// returns the mean and minimum luma PSNR over it.
func decodeSoakChunk(t *testing.T, tool string, frames [][]byte, firstIdx int) (mean, min float64) {
	t.Helper()
	data, err := ivf.Marshal(ivf.Config{
		Width: soakWidth, Height: soakHeight,
		FPSNumerator: soakFPS, FPSDenominator: 1,
	}, frames)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := vp8check.Decode(tool, data, soakWidth, soakHeight)
	if err != nil {
		t.Fatalf("the reference decoder rejected frames %d..%d: %v",
			firstIdx, firstIdx+len(frames)-1, err)
	}
	if len(decoded) != len(frames) {
		t.Fatalf("frames %d..%d: decoded %d pictures, want %d",
			firstIdx, firstIdx+len(frames)-1, len(decoded), len(frames))
	}
	min = 999
	for i, got := range decoded {
		want := soakFrame(firstIdx + i)[:soakWidth*soakHeight]
		psnr, err := vp8check.PSNR(got.Y, want)
		if err != nil {
			t.Fatalf("frame %d: PSNR: %v", firstIdx+i, err)
		}
		mean += psnr
		if psnr < min {
			min = psnr
		}
	}
	return mean / float64(len(decoded)), min
}

// TestSoakRunIsStable encodes 96 seconds of 640x480 video and checks three
// things that only a long run can check: every frame stays decodable, quality
// does not decay as key-frame cycles accumulate, and neither the heap nor the
// goroutine count grows with the frame count.
//
// It is opt-in: set VP8_SOAK=1 to run it. The CI job runs this package under
// -race with coverage instrumentation and already spends ten minutes there, and
// a soak long enough to be worth running costs another ten on that hardware —
// too much to pay on every pull request for a check whose purpose is to be run
// deliberately before a release.
func TestSoakRunIsStable(t *testing.T) {
	if os.Getenv("VP8_SOAK") != "1" {
		t.Skip("soak run takes minutes; set VP8_SOAK=1 to run it")
	}
	if testing.Short() {
		t.Skip("soak run takes minutes; skipped under -short")
	}
	tool := requireTool(t)

	enc, err := vp8.NewEncoder(soakWidth, soakHeight, soakFPS)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	enc.SetKeyFrameInterval(soakKeyInterval)
	enc.SetRateControl(true)
	enc.SetBitrate(soakBitrate)

	var (
		chunk        = make([][]byte, 0, soakChunk)
		chunkFirst   int
		chunkMeans   []float64
		keyFrames    int
		totalBytes   int
		baseHeap     uint64
		baseRoutines int
	)

	for i := 0; i < soakFrames; i++ {
		frame, err := enc.Encode(soakFrame(i))
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		if len(frame) == 0 {
			t.Fatalf("frame %d: encoder produced no bytes", i)
		}
		if key := isKeyFrame(frame); key {
			keyFrames++
			if i%soakKeyInterval != 0 {
				t.Fatalf("frame %d is a key frame, off the %d-frame cadence",
					i, soakKeyInterval)
			}
		} else if i%soakKeyInterval == 0 {
			t.Fatalf("frame %d should have been a key frame", i)
		}
		totalBytes += len(frame)
		chunk = append(chunk, frame)

		if len(chunk) < soakChunk {
			continue
		}
		mean, min := decodeSoakChunk(t, tool, chunk, chunkFirst)
		t.Logf("frames %4d..%4d: mean luma PSNR %.2f dB, worst %.2f dB",
			chunkFirst, i, mean, min)
		if min < 25 {
			t.Errorf("frames %d..%d: worst luma PSNR %.2f dB is too low for a faithful encode",
				chunkFirst, i, min)
		}
		chunkMeans = append(chunkMeans, mean)

		// Take the memory baseline after the first chunk, so one-time
		// allocations (reference buffers, worker stacks, the decoder's own
		// warm-up) are already counted and only growth shows up.
		chunk = chunk[:0]
		chunkFirst = i + 1
		if len(chunkMeans) == 1 {
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			baseHeap = ms.HeapAlloc
			baseRoutines = runtime.NumGoroutine()
		}
	}

	if len(chunk) != 0 {
		t.Fatalf("%d frames left over: soakFrames must be a multiple of soakChunk", len(chunk))
	}
	if want := soakFrames / soakKeyInterval; keyFrames != want {
		t.Errorf("produced %d key frames, want %d", keyFrames, want)
	}

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Log(summariseSoak(totalBytes, baseHeap, ms.HeapAlloc, baseRoutines, runtime.NumGoroutine()))

	// The encoder holds a fixed working set: the source frame, the
	// reconstruction and three reference buffers. Nothing it does should scale
	// with how long it has been running, so allow slack for allocator noise
	// but not for a per-frame retention.
	if ms.HeapAlloc > baseHeap*2+4<<20 {
		t.Errorf("heap grew from %.1f MiB to %.1f MiB over %d frames, which suggests a per-frame retention",
			float64(baseHeap)/(1<<20), float64(ms.HeapAlloc)/(1<<20), soakFrames)
	}
	if n := runtime.NumGoroutine(); n > baseRoutines+2 {
		t.Errorf("goroutine count grew from %d to %d: wavefront workers are not being reaped",
			baseRoutines, n)
	}

	// Rate control has to hold over the whole run, not just settle early: a
	// controller that gives ground each key-frame cycle would drift above the
	// target without any single frame looking wrong.
	meanBitrate := float64(totalBytes) * 8 * soakFPS / float64(soakFrames)
	if meanBitrate > 1.5*soakBitrate {
		t.Errorf("mean bitrate %.2f Mbps over the run, target %.2f Mbps: rate control is not holding",
			meanBitrate/1e6, soakBitrate/1e6)
	}

	// Drift check. Quality is measured against the source, so a reconstruction
	// that slowly diverges from what the decoder sees shows up as the later
	// chunks scoring below the earlier ones. Each chunk starts on a key frame,
	// which bounds how far any single drift can travel — but a systematic
	// error would still tilt the series downward.
	first, last := chunkMeans[0], chunkMeans[len(chunkMeans)-1]
	if last < first-1.0 {
		t.Errorf("mean luma PSNR fell from %.2f dB in the first chunk to %.2f dB in the last: quality is decaying over the run",
			first, last)
	}
}

func summariseSoak(totalBytes int, baseHeap, endHeap uint64, baseRoutines, endRoutines int) string {
	seconds := float64(soakFrames) / soakFPS
	return fmt.Sprintf(
		"soak: %d frames in %.0fs of video, %.2f Mbps mean, heap %.1f -> %.1f MiB, goroutines %d -> %d",
		soakFrames, seconds, float64(totalBytes)*8/seconds/1e6,
		float64(baseHeap)/(1<<20), float64(endHeap)/(1<<20), baseRoutines, endRoutines)
}
