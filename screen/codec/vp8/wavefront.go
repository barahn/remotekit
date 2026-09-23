// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import (
	"runtime"
	"sync"
)

// Wavefront scheduling for the macroblock analysis pass.
//
// Closing the prediction loop made the pass serial: a macroblock cannot be
// analysed until its neighbours are reconstructed. That is true, and it does
// not mean the whole frame must be one goroutine. A macroblock at (x, y) reads
// four already-finished neighbours — (x-1, y), (x-1, y-1), (x, y-1) and, for
// the above-right pixels B_PRED predicts along, (x+1, y-1). Nothing else. So a
// row may start as soon as the row above it is two macroblocks ahead, and rows
// run concurrently in a diagonal wave rather than one after another.
//
// The dependency set is the same for inter frames: findNearMVs reads the above,
// left and above-left macroblocks' motion vectors, all of which the same
// condition already covers.
//
// What this does NOT permit is the open-loop shortcut this encoder used to
// take — analysing from the source frame so every macroblock is independent.
// That was faster and wrong, and it capped quality at a level no quantiser
// could reach.

// wavefrontThreshold is the macroblock count below which the pass stays on one
// goroutine. The coordination costs a mutex round per macroblock, which is not
// worth paying on a frame small enough to finish in a millisecond.
var wavefrontThreshold = 512

// wavefront coordinates rows of macroblocks against the two-ahead rule.
type wavefront struct {
	mu   sync.Mutex
	cond *sync.Cond
	// done[y] is how many macroblocks of row y have been finished.
	done []int
	// cols is the row length, which is also the most done[y] can ever reach.
	cols int
}

func newWavefront(rows, cols int) *wavefront {
	w := &wavefront{done: make([]int, rows), cols: cols}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// waitFor blocks until the macroblock at (x, y) may be analysed.
func (w *wavefront) waitFor(x, y int) {
	if y == 0 {
		return // the first row depends on nothing above it
	}
	// Two ahead, but never more than the row above can offer. The last
	// macroblock of a row would otherwise ask for mbW+1 completions from a row
	// that can only ever reach mbW, and wait forever — which is a deadlock, not
	// a slow encode, and it is what the first version of this did.
	need := x + 2
	if need > w.cols {
		need = w.cols
	}
	w.mu.Lock()
	for w.done[y-1] < need {
		w.cond.Wait()
	}
	w.mu.Unlock()
}

// complete records that (x, y) is finished and releases anything waiting on it.
func (w *wavefront) complete(x, y int) {
	w.mu.Lock()
	w.done[y] = x + 1
	w.mu.Unlock()
	w.cond.Broadcast()
}

// forEachMacroblockInWave runs fn over every macroblock, in an order that
// respects the two-ahead rule, using as many goroutines as there are rows and
// processors.
//
// fn is called exactly once per macroblock, and for a given macroblock only
// after its four neighbours have returned — so fn may read what they wrote.
func forEachMacroblockInWave(mbW, mbH int, fn func(mbX, mbY int)) {
	if mbW*mbH < wavefrontThreshold || runtime.GOMAXPROCS(0) < 2 || mbH < 2 {
		for y := 0; y < mbH; y++ {
			for x := 0; x < mbW; x++ {
				fn(x, y)
			}
		}
		return
	}

	w := newWavefront(mbH, mbW)
	workers := runtime.GOMAXPROCS(0)
	if workers > mbH {
		workers = mbH
	}

	// Rows are handed out statically, strided across workers. A row cannot
	// outrun the row above it whoever owns it, so the assignment only decides
	// who waits, not what the result is.
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(start int) {
			defer wg.Done()
			for y := start; y < mbH; y += workers {
				for x := 0; x < mbW; x++ {
					w.waitFor(x, y)
					fn(x, y)
					w.complete(x, y)
				}
			}
		}(i)
	}
	wg.Wait()
}
