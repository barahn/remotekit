package vp8

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// parallelRowThreshold is the macroblock count below which the analysis pass
// stays on one goroutine. Below roughly this size the scheduling costs more
// than the work saved; 512 macroblocks is about 640x360.
//
// A var rather than a const so that TestParallelAnalysisIsBitExact can force
// both paths over the same frame and compare them.
var parallelRowThreshold = 512

// forEachRow runs fn once per macroblock row, in parallel when the frame is
// large enough to be worth it.
//
// This is safe only where it is used: the key-frame analysis pass. There, every
// macroblock reads the SOURCE frame and its own neighbours in the source, and
// writes only its own slot — so no macroblock can observe another's result and
// the output does not depend on the order rows complete in. The inter-frame
// pass is NOT like this: findNearMVs reads the motion vectors of macroblocks
// decided earlier in raster order, which is a real dependency and must stay
// serial.
//
// Rows are claimed from a shared counter rather than split into equal blocks,
// because rows differ in cost — a row of flat background finishes far sooner
// than a row of text — and a static split would leave workers idle waiting for
// the slowest block.
func forEachRow(rows, totalMBs int, fn func(row int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > rows {
		workers = rows
	}
	if workers < 2 || totalMBs < parallelRowThreshold {
		for row := 0; row < rows; row++ {
			fn(row)
		}
		return
	}

	var next atomic.Int64
	next.Store(-1)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				row := int(next.Add(1))
				if row >= rows {
					return
				}
				fn(row)
			}
		}()
	}
	wg.Wait()
}
