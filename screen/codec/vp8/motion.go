// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
// Forked from github.com/opd-ai/vp8 (MIT); see LICENSE.upstream.

package vp8

// This file implements VP8 motion estimation and motion vector types.
// Motion estimation finds the best matching block in a reference frame
// for each macroblock in the current frame.
//
// VP8 motion vectors are specified with quarter-pixel precision per RFC 6386 §18.
// This implementation performs integer-pixel motion search only (motion vectors
// are multiples of 4 quarter-pixels). Sub-pixel refinement may be added in future.

// motionVector represents a VP8 motion vector with quarter-pel precision.
// The values are in quarter-pixel units: dx=4 means 1 full pixel to the right.
type motionVector struct {
	dx int16 // horizontal displacement in quarter pixels
	dy int16 // vertical displacement in quarter pixels
}

// zeroMV is the zero motion vector (no displacement).
var zeroMV = motionVector{0, 0}

// mvEqual returns true if two motion vectors are equal.
func mvEqual(a, b motionVector) bool {
	return a.dx == b.dx && a.dy == b.dy
}

// snapMVTo2Pel snaps a motion vector to the nearest 2-pixel grid
// (multiples of 8 quarter-pixel units). This ensures that chroma MVs
// derived by halving the luma MV always land on integer-pixel positions,
// avoiding encoder/decoder mismatch from sub-pel truncation.
func snapMVTo2Pel(mv motionVector) motionVector {
	return motionVector{
		dx: snapComponentTo2Pel(mv.dx),
		dy: snapComponentTo2Pel(mv.dy),
	}
}

// snapComponentTo2Pel rounds a single MV component to the nearest
// multiple of 8 quarter-pixel units. Negative values are rounded away
// from zero (symmetric with positive rounding) to maintain consistent
// search behavior in all directions.
func snapComponentTo2Pel(v int16) int16 {
	if v >= 0 {
		return ((v + 4) / 8) * 8
	}
	return -((-v + 4) / 8) * 8
}

// interMode represents the VP8 inter-frame macroblock prediction mode.
type interMode uint8

const (
	// mvModeNearestMV uses the nearest reference motion vector.
	mvModeNearestMV interMode = iota
	// mvModeNearMV uses the near (second-closest) reference motion vector.
	mvModeNearMV
	// mvModeZeroMV uses a zero motion vector (no motion).
	mvModeZeroMV
	// mvModeNewMV uses a new motion vector encoded explicitly.
	mvModeNewMV
)

// mvSearchRange defines the maximum motion vector search range in pixels.
// VP8 supports up to ±1023.75 quarter pixels = ±255.9375 pixels.
const mvSearchRange = 64

// motionEstimateResult holds the result of motion estimation for one macroblock.
type motionEstimateResult struct {
	mv   motionVector
	sad  int
	mode interMode
}

// estimateMotion performs motion estimation for a 16x16 macroblock.
// It searches in the reference frame for the best matching block using
// a diamond search pattern, starting from the predicted motion vector.
//
// Parameters:
//   - srcY: source luma block (256 bytes, 16x16)
//   - ref: reference frame luma plane
//   - refW, refH: reference frame dimensions
//   - mbX, mbY: macroblock position (pixel coordinates of top-left corner)
//   - predMV: predicted motion vector (from neighbors)
//
// Returns the best motion vector and its SAD cost.
// estimateZeroMotion is the screen-content path: it evaluates MV (0,0) and
// nothing else.
//
// Two reasons, and the second is the load-bearing one.
//
// Screen content is mostly static between frames - a cursor moves, one window
// repaints, the rest is identical - so a motion search spends its time
// confirming that the best vector is zero.
//
// More importantly it never produces MV_NEW, and MV_NEW is where this
// encoder's inherited defects live: the bitstream codes only a delta against
// the *decoder-derived* predictor, and findNearestMV is documented upstream as
// possibly diverging from RFC 6386 section 18.2. Measured through libvpx, that
// path reconstructs at 8-12 dB. Staying on ZEROMV designs the defect class out
// rather than avoiding it by luck. See pkg/screen/codec/vp8/README.md.
func estimateZeroMotion(srcY, ref []byte, refW, refH, mbX, mbY int) motionEstimateResult {
	return motionEstimateResult{
		mv:   zeroMV,
		sad:  computeMCSAD16x16(srcY, ref, refW, refH, mbX, mbY, zeroMV),
		mode: mvModeZeroMV,
	}
}

func estimateMotion(srcY, ref []byte, refW, refH, mbX, mbY int, predMV motionVector) motionEstimateResult {
	// Snap predicted MV to 2-pel grid (multiples of 8 qpel) so that
	// chroma MVs (halved from luma) always land on integer pixels.
	snappedPred := snapMVTo2Pel(predMV)

	// Start with the snapped predicted MV
	bestMV := snappedPred
	bestSAD := computeMCSAD16x16(srcY, ref, refW, refH, mbX, mbY, snappedPred)

	// Also evaluate zero MV
	if !mvEqual(snappedPred, zeroMV) {
		zeroSAD := computeMCSAD16x16(srcY, ref, refW, refH, mbX, mbY, zeroMV)
		if zeroSAD < bestSAD {
			bestMV = zeroMV
			bestSAD = zeroSAD
		}
	}

	// Diamond search around the best initial point (all steps are 2-pel)
	bestMV, bestSAD = diamondSearch(srcY, ref, refW, refH, mbX, mbY, bestMV, bestSAD)

	// Determine the inter mode based on the selected MV
	result := motionEstimateResult{
		mv:  bestMV,
		sad: bestSAD,
	}
	if mvEqual(bestMV, zeroMV) {
		result.mode = mvModeZeroMV
	} else {
		result.mode = mvModeNewMV
	}

	return result
}

// diamondSearch performs a diamond search pattern to refine a motion vector.
// It iteratively evaluates neighboring positions in a diamond pattern,
// moving to the best position until no improvement is found.
//
// All search offsets are multiples of 8 quarter-pixel units (2 full pixels)
// to ensure that chroma MVs (halved from luma) remain at integer-pel precision,
// avoiding encoder/decoder mismatch from sub-pel truncation.
func diamondSearch(srcY, ref []byte, refW, refH, mbX, mbY int, startMV motionVector, startSAD int) (motionVector, int) {
	bestMV := startMV
	bestSAD := startSAD

	// Large diamond search (up to maxLargeDiamondSteps iterations)
	bestMV, bestSAD = largeDiamondSearch(srcY, ref, refW, refH, mbX, mbY, bestMV, bestSAD)

	// Small diamond refinement
	bestMV, bestSAD = smallDiamondSearch(srcY, ref, refW, refH, mbX, mbY, bestMV, bestSAD)

	return bestMV, bestSAD
}

// largeDiamondSearch performs large diamond pattern search (8-point).
func largeDiamondSearch(srcY, ref []byte, refW, refH, mbX, mbY int, startMV motionVector, startSAD int) (motionVector, int) {
	// Large diamond pattern offsets (in quarter-pixel units: 8 qpel = 2 pixels)
	largeDiamond := [8]motionVector{
		{0, -8},
		{0, 8},
		{-8, 0},
		{8, 0},
		{-8, -8},
		{8, -8},
		{-8, 8},
		{8, 8},
	}

	bestMV := startMV
	bestSAD := startSAD

	const maxLargeDiamondSteps = 16
	for step := 0; step < maxLargeDiamondSteps; step++ {
		improved := false
		for _, delta := range largeDiamond {
			newMV, newSAD, found := evaluateMVCandidate(srcY, ref, refW, refH, mbX, mbY, bestMV, delta, bestSAD)
			if found {
				bestMV = newMV
				bestSAD = newSAD
				improved = true
			}
		}
		if !improved {
			break
		}
	}
	return bestMV, bestSAD
}

// smallDiamondSearch performs small diamond pattern search (4-point).
func smallDiamondSearch(srcY, ref []byte, refW, refH, mbX, mbY int, startMV motionVector, startSAD int) (motionVector, int) {
	smallDiamond := [4]motionVector{
		{0, -8}, {0, 8}, {-8, 0}, {8, 0},
	}

	bestMV := startMV
	bestSAD := startSAD

	for i := 0; i < 100; i++ {
		improved := false
		for _, delta := range smallDiamond {
			newMV, newSAD, found := evaluateMVCandidate(srcY, ref, refW, refH, mbX, mbY, bestMV, delta, bestSAD)
			if found {
				bestMV = newMV
				bestSAD = newSAD
				improved = true
			}
		}
		if !improved {
			break
		}
	}
	return bestMV, bestSAD
}

// evaluateMVCandidate evaluates a candidate MV and returns it if better.
func evaluateMVCandidate(srcY, ref []byte, refW, refH, mbX, mbY int, baseMV, delta motionVector, currentBestSAD int) (motionVector, int, bool) {
	candidateMV := motionVector{
		dx: baseMV.dx + delta.dx,
		dy: baseMV.dy + delta.dy,
	}
	if !mvInRange(candidateMV, mbX, mbY, refW, refH) {
		return motionVector{}, 0, false
	}
	sad := computeMCSAD16x16(srcY, ref, refW, refH, mbX, mbY, candidateMV)
	if sad < currentBestSAD {
		return candidateMV, sad, true
	}
	return motionVector{}, 0, false
}

// mvInRange checks whether applying the motion vector at the given macroblock
// position results in a valid reference block (fully within the reference frame).
func mvInRange(mv motionVector, mbX, mbY, refW, refH int) bool {
	// Convert quarter-pel MV to integer pixel offset (truncate toward zero)
	dxPel := int(mv.dx) / 4
	dyPel := int(mv.dy) / 4

	refX := mbX + dxPel
	refY := mbY + dyPel

	if refX < 0 || refY < 0 {
		return false
	}
	if refX+16 > refW || refY+16 > refH {
		return false
	}
	return true
}

// computeMCSAD16x16 computes the SAD between a source block and a
// motion-compensated reference block.
func computeMCSAD16x16(srcY, ref []byte, refW, refH, mbX, mbY int, mv motionVector) int {
	// Integer pixel offset (truncate quarter-pel to full pixel)
	dxPel := int(mv.dx) / 4
	dyPel := int(mv.dy) / 4

	refX := mbX + dxPel
	refY := mbY + dyPel

	// Boundary check
	if refX < 0 || refY < 0 || refX+16 > refW || refY+16 > refH {
		return 1 << 30 // Very large SAD for out-of-bounds
	}

	sad := 0
	for row := 0; row < 16; row++ {
		srcOff := row * 16
		refOff := (refY+row)*refW + refX
		for col := 0; col < 16; col++ {
			diff := int(srcY[srcOff+col]) - int(ref[refOff+col])
			if diff < 0 {
				diff = -diff
			}
			sad += diff
		}
	}
	return sad
}

// motionCompensate16x16 copies a 16x16 block from the reference frame at
// the position offset by the motion vector. The result is stored in dst.
func motionCompensate16x16(dst, ref []byte, refW, refH, mbX, mbY int, mv motionVector) {
	dxPel := int(mv.dx) / 4
	dyPel := int(mv.dy) / 4
	refX := mbX + dxPel
	refY := mbY + dyPel
	copyBlockClamped(dst, ref, refW, refH, refX, refY, 16, 16)
}

// motionCompensate8x8 copies an 8x8 block from the reference frame at
// the position offset by the motion vector. Used for chroma planes.
func motionCompensate8x8(dst, ref []byte, refW, refH, mbX, mbY int, mv motionVector) {
	dxPel := int(mv.dx) / 4
	dyPel := int(mv.dy) / 4
	refX := mbX + dxPel
	refY := mbY + dyPel
	copyBlockClamped(dst, ref, refW, refH, refX, refY, 8, 8)
}

// copyBlockClamped copies a block from ref to dst, clamping coordinates to valid range.
func copyBlockClamped(dst, ref []byte, refW, refH, refX, refY, blockW, blockH int) {
	for row := 0; row < blockH; row++ {
		srcRow := clampCoord(refY+row, refH)
		for col := 0; col < blockW; col++ {
			srcCol := clampCoord(refX+col, refW)
			dst[row*blockW+col] = ref[srcRow*refW+srcCol]
		}
	}
}

// clampCoord clamps a coordinate to the valid range [0, max-1].
func clampCoord(val, max int) int {
	if val < 0 {
		return 0
	}
	if val >= max {
		return max - 1
	}
	return val
}

// getDiagonalNeighborIndex returns the index of the diagonal neighbor MB.
// Returns -1 if no diagonal neighbor is available.
func getDiagonalNeighborIndex(mbX, mbY, mbW int) int {
	if mbX < mbW-1 {
		return (mbY-1)*mbW + (mbX + 1) // above-right
	}
	if mbX > 0 {
		return (mbY-1)*mbW + (mbX - 1) // above-left
	}
	return -1
}

// mvCost estimates the bit cost of encoding a motion vector difference.
// This is used in mode decision to compare inter modes.
func mvCost(mv, predMV motionVector) int {
	dMVx := int(mv.dx) - int(predMV.dx)
	dMVy := int(mv.dy) - int(predMV.dy)

	if dMVx < 0 {
		dMVx = -dMVx
	}
	if dMVy < 0 {
		dMVy = -dMVy
	}

	// Approximate bit cost: each component costs ~1 + log2(abs(delta)) bits
	cost := 0
	cost += mvComponentCost(dMVx)
	cost += mvComponentCost(dMVy)

	// Weight by a factor to make comparable with SAD
	return cost * 4
}

// mvComponentCost estimates bits needed to encode one MV component.
func mvComponentCost(absVal int) int {
	if absVal == 0 {
		return 1
	}
	bits := 2 // sign + at least one magnitude bit
	v := absVal
	for v > 1 {
		v >>= 1
		bits++
	}
	return bits
}
