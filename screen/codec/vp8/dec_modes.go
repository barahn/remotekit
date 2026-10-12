// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

// The trees the first partition is read with, in libvpx's representation
// (see boolDecoder.readTree): RFC 6386 §8.1, §11.2-11.4, §16, §17.

var (
	// kfYModeTree splits B_PRED off first; key frames only.
	kfYModeTree = []int8{-int8(B_PRED), 2, 4, 6, -int8(DC_PRED), -int8(V_PRED), -int8(H_PRED), -int8(TM_PRED)}
	// yModeTree splits DC_PRED off first; intra macroblocks in inter frames.
	yModeTree  = []int8{-int8(DC_PRED), 2, 4, 6, -int8(V_PRED), -int8(H_PRED), -int8(TM_PRED), -int8(B_PRED)}
	uvModeTree = []int8{0, 2, -1, 4, -2, -3} // DC, V, H, TM
	bModeTree  = []int8{
		-int8(B_DC_PRED), 2,
		-int8(B_TM_PRED), 4,
		-int8(B_VE_PRED), 6,
		8, 12,
		-int8(B_HE_PRED), 10,
		-int8(B_RD_PRED), -int8(B_VR_PRED),
		-int8(B_LD_PRED), 14,
		-int8(B_VL_PRED), 16,
		-int8(B_HD_PRED), -int8(B_HU_PRED),
	}
	// smallMVTree codes the magnitudes 0-7.
	smallMVTree = []int8{2, 8, 4, 6, 0, -1, -2, -3, 10, 12, -4, -5, -6, -7}
	// splitTree picks the split partitioning: 16 4x4s, four 8x8s, two 16x8s
	// (top and bottom) or two 8x16s (left and right).
	splitTree  = []int8{-splitQuarters4x4, 2, -splitQuarters8x8, 4, -splitTopBottom, -splitLeftRight}
	splitProbs = []uint8{110, 111, 150}
	// subMVTree is a split partition's motion vector: the left neighbour's,
	// the above neighbour's, zero, or a new one.
	subMVTree = []int8{-subMVLeft, 2, -subMVAbove, 4, -subMVZero, -subMVNew}
)

// kfYModeProb is the fixed key-frame luma mode probability row.
var kfYModeProb = [4]uint8{145, 156, 163, 128}

// The split partitionings, numbered as libvpx's vp8_mbsplits.
const (
	splitTopBottom = iota
	splitLeftRight
	splitQuarters8x8
	splitQuarters4x4
)

// splitMaps assigns each 4x4 sub-block to its partition, per partitioning.
var splitMaps = [4][16]uint8{
	{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1},
	{0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1},
	{0, 0, 1, 1, 0, 0, 1, 1, 2, 2, 3, 3, 2, 2, 3, 3},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
}

var splitCounts = [4]int{2, 2, 4, 16}

const (
	subMVLeft = iota
	subMVAbove
	subMVZero
	subMVNew
)

// subMVProbs are indexed by how the left and above sub-block vectors relate
// (libvpx vp8_sub_mv_ref_prob2).
var subMVProbs = [5][3]uint8{
	{147, 136, 18}, // normal
	{106, 145, 1},  // left is zero
	{179, 121, 1},  // above is zero
	{223, 1, 34},   // left and above are the same
	{208, 1, 1},    // left and above are both zero
}

// readModes reads every macroblock's modes and motion vectors from the rest of
// the first partition, in raster order.
func (d *Decoder) readModes(br *boolDecoder, h *frameHeader) {
	for i := range d.intraTop {
		d.intraTop[i] = uint8(B_DC_PRED)
	}
	for mbY := 0; mbY < d.mbH; mbY++ {
		var intraLeft [4]uint8
		for i := range intraLeft {
			intraLeft[i] = uint8(B_DC_PRED)
		}
		for mbX := 0; mbX < d.mbW; mbX++ {
			mb := &d.mbs[mbY*d.mbW+mbX]
			if h.updateSeg {
				if br.readBool(d.segmentProbs[0]) {
					mb.segment = 2
					if br.readBool(d.segmentProbs[2]) {
						mb.segment = 3
					}
				} else {
					mb.segment = 0
					if br.readBool(d.segmentProbs[1]) {
						mb.segment = 1
					}
				}
			} else if h.keyFrame {
				// "By default on a key frame reset all MBs to segment 0."
				mb.segment = 0
			}
			mb.skip = h.skipProb >= 0 && br.readBool(uint8(h.skipProb))

			if h.keyFrame {
				d.readKeyFrameModes(br, mb, mbX, &intraLeft)
			} else {
				d.readInterFrameModes(br, h, mb, mbX, mbY)
			}
		}
	}
}

// readKeyFrameModes reads an intra macroblock in a key frame, whose sub-block
// modes are coded in the context of the ones above and to the left.
func (d *Decoder) readKeyFrameModes(br *boolDecoder, mb *decMB, mbX int, left *[4]uint8) {
	mb.ref = refIntra
	mb.mvs = [16]motionVector{}
	mb.yMode = uint8(br.readTree(kfYModeTree, kfYModeProb[:]))
	top := d.intraTop[4*mbX : 4*mbX+4]
	if intraMode(mb.yMode) == B_PRED {
		for i := 0; i < 16; i++ {
			a, l := top[i&3], left[i>>2]
			m := uint8(br.readTree(bModeTree, kfBModeProb[a][l][:]))
			mb.bModes[i] = intraBMode(m)
			top[i&3], left[i>>2] = m, m
		}
	} else {
		m := bModeForLumaMode(intraMode(mb.yMode))
		for i := range mb.bModes {
			mb.bModes[i] = m
		}
		for i := 0; i < 4; i++ {
			top[i], left[i] = uint8(m), uint8(m)
		}
	}
	mb.uvMode = uint8(br.readTree(uvModeTree, kfUVModeProb[:]))
}

// readInterFrameModes reads a macroblock in an inter frame: intra with fixed
// probabilities, or inter with a reference and motion vectors predicted from
// its neighbours.
func (d *Decoder) readInterFrameModes(br *boolDecoder, h *frameHeader, mb *decMB, mbX, mbY int) {
	if !br.readBool(h.probIntra) {
		mb.ref = refIntra
		mb.mvs = [16]motionVector{}
		mb.yMode = uint8(br.readTree(yModeTree, d.yModeProbs[:]))
		if intraMode(mb.yMode) == B_PRED {
			for i := range mb.bModes {
				mb.bModes[i] = intraBMode(br.readTree(bModeTree, interBModeProb[:]))
			}
		}
		mb.uvMode = uint8(br.readTree(uvModeTree, d.uvModeProb[:]))
		return
	}

	mb.ref = refLast
	if br.readBool(h.probLast) {
		mb.ref = refGolden
		if br.readBool(h.probGolden) {
			mb.ref = refAltRef
		}
	}
	mb.uvMode = 0

	near, nearest, best, counts := d.findNearMVs(mb.ref, mbX, mbY)
	probs := [4]uint8{
		modeContexts[counts[0]][0],
		modeContexts[counts[1]][1],
		modeContexts[counts[2]][2],
		modeContexts[counts[3]][3],
	}
	var mv motionVector
	switch {
	case !br.readBool(probs[0]):
		mb.yMode = modeZero
	case !br.readBool(probs[1]):
		mb.yMode = modeNearest
		mv = nearest
	case !br.readBool(probs[2]):
		mb.yMode = modeNear
		mv = near
	case !br.readBool(probs[3]):
		mb.yMode = modeNew
		mv = d.readMV(br)
		mv.dy += best.dy
		mv.dx += best.dx
	default:
		mb.yMode = modeSplit
		d.readSplitMVs(br, mb, mbX, mbY, best)
		return
	}
	for i := range mb.mvs {
		mb.mvs[i] = mv
	}
}

// findNearMVs derives the nearest, near and best motion vectors and the
// neighbour counts that pick the mode probabilities (libvpx vp8_find_near_mvs,
// RFC 6386 §16.3). The three vectors come back clamped to the frame.
func (d *Decoder) findNearMVs(ref uint8, mbX, mbY int) (near, nearest, best motionVector, counts [4]int) {
	const (
		cIntra = iota
		cNearest
		cNear
		cSplit
	)
	var mvs [4]motionVector
	n := 0 // index of the last vector added
	consider := func(nb *decMB, weight int) {
		if nb == nil || nb.ref == refIntra {
			return
		}
		mv := nb.mvs[15]
		if mv == (motionVector{}) {
			counts[cIntra] += weight
			return
		}
		if d.signBias[nb.ref] != d.signBias[ref] {
			mv.dy, mv.dx = -mv.dy, -mv.dx
		}
		if n == 0 || mv != mvs[n] {
			n++
			mvs[n] = mv
		}
		counts[n] += weight
	}
	var above, left, aboveLeft *decMB
	if mbY > 0 {
		above = &d.mbs[(mbY-1)*d.mbW+mbX]
		if mbX > 0 {
			aboveLeft = &d.mbs[(mbY-1)*d.mbW+mbX-1]
		}
	}
	if mbX > 0 {
		left = &d.mbs[mbY*d.mbW+mbX-1]
	}
	// The above neighbour is always added when it is non-zero; libvpx does
	// not compare it against anything.
	if above != nil && above.ref != refIntra {
		mv := above.mvs[15]
		if mv != (motionVector{}) {
			if d.signBias[above.ref] != d.signBias[ref] {
				mv.dy, mv.dx = -mv.dy, -mv.dx
			}
			n++
			mvs[n] = mv
		}
		counts[n] += 2
	}
	consider(left, 2)
	consider(aboveLeft, 1)

	// Three distinct vectors, and the last equals the nearest: merge it in.
	if counts[cSplit] > 0 && mvs[n] == mvs[cNearest] {
		counts[cNearest]++
	}
	counts[cSplit] = 0
	if above != nil && above.yMode == modeSplit {
		counts[cSplit] += 2
	}
	if left != nil && left.yMode == modeSplit {
		counts[cSplit] += 2
	}
	if aboveLeft != nil && aboveLeft.yMode == modeSplit {
		counts[cSplit]++
	}
	if counts[cNear] > counts[cNearest] {
		counts[cNear], counts[cNearest] = counts[cNearest], counts[cNear]
		mvs[cNear], mvs[cNearest] = mvs[cNearest], mvs[cNear]
	}
	if counts[cNearest] >= counts[cIntra] {
		mvs[cIntra] = mvs[cNearest]
	}
	best = d.clampMV(mvs[cIntra], mbX, mbY)
	nearest = d.clampMV(mvs[cNearest], mbX, mbY)
	near = d.clampMV(mvs[cNear], mbX, mbY)
	return near, nearest, best, counts
}

// clampMV keeps a predicted vector within 16 pixels past the frame's edges
// (libvpx vp8_clamp_mv2). Units are quarter pixels.
func (d *Decoder) clampMV(mv motionVector, mbX, mbY int) motionVector {
	const margin = 16 * 4
	toLeft, toRight := -mbX*64, (d.mbW-1-mbX)*64
	toTop, toBottom := -mbY*64, (d.mbH-1-mbY)*64
	mv.dx = int16(clampInt(int(mv.dx), toLeft-margin, toRight+margin))
	mv.dy = int16(clampInt(int(mv.dy), toTop-margin, toBottom+margin))
	return mv
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// readMV reads a motion vector, rows first.
func (d *Decoder) readMV(br *boolDecoder) motionVector {
	dy := readMVComponent(br, &d.mvProbs[0])
	dx := readMVComponent(br, &d.mvProbs[1])
	return motionVector{dx: int16(dx), dy: int16(dy)}
}

// readMVComponent reads one component in quarter pixels (RFC 6386 §17.1).
func readMVComponent(br *boolDecoder, p *[19]uint8) int {
	const (
		isShort = 0
		sign    = 1
		short   = 2
		long    = 9
		bits    = 10
	)
	x := 0
	if br.readBool(p[isShort]) {
		for i := 0; i < 3; i++ {
			if br.readBool(p[long+i]) {
				x += 1 << uint(i)
			}
		}
		for i := bits - 1; i > 3; i-- {
			if br.readBool(p[long+i]) {
				x += 1 << uint(i)
			}
		}
		// Bit 3 is implicit when nothing above it is set: a long vector
		// is at least 8.
		if x&0xfff0 == 0 || br.readBool(p[long+3]) {
			x += 8
		}
	} else {
		x = br.readTree(smallMVTree, p[short:short+7])
	}
	if x != 0 && br.readBool(p[sign]) {
		x = -x
	}
	return x
}

// readSplitMVs reads a SPLITMV macroblock's partitioning and one vector per
// partition (libvpx decode_split_mv).
func (d *Decoder) readSplitMVs(br *boolDecoder, mb *decMB, mbX, mbY int, best motionVector) {
	s := br.readTree(splitTree, splitProbs)
	mb.split = uint8(s)
	m := &splitMaps[s]
	var left, above *decMB
	if mbX > 0 {
		left = &d.mbs[mbY*d.mbW+mbX-1]
	}
	if mbY > 0 {
		above = &d.mbs[(mbY-1)*d.mbW+mbX]
	}
	for part := 0; part < splitCounts[s]; part++ {
		k := 0
		for m[k] != uint8(part) {
			k++
		}
		// The neighbours' vectors are taken as they are: no sign bias
		// correction, unlike findNearMVs. An intra or absent neighbour
		// counts as zero.
		var l, a motionVector
		if k&3 != 0 {
			l = mb.mvs[k-1]
		} else if left != nil && left.ref != refIntra {
			l = left.mvs[k+3]
		}
		if k>>2 != 0 {
			a = mb.mvs[k-4]
		} else if above != nil && above.ref != refIntra {
			a = above.mvs[k+12]
		}
		ctx := 0
		switch {
		case l == a:
			ctx = 3
			if a == (motionVector{}) {
				ctx = 4
			}
		case a == (motionVector{}):
			ctx = 2
		case l == (motionVector{}):
			ctx = 1
		}
		var mv motionVector
		switch br.readTree(subMVTree, subMVProbs[ctx][:]) {
		case subMVLeft:
			mv = l
		case subMVAbove:
			mv = a
		case subMVNew:
			mv = d.readMV(br)
			mv.dy += best.dy
			mv.dx += best.dx
		}
		for i := k; i < 16; i++ {
			if m[i] == uint8(part) {
				mb.mvs[i] = mv
			}
		}
	}
}
