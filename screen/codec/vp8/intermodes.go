package vp8

// This file implements the inter-frame macroblock mode and motion-vector layer
// of RFC 6386 §16–18.
//
// The distinguishing property of this layer is that almost nothing in it is the
// encoder's choice. The decoder derives the motion-vector predictors from the
// neighbouring macroblocks it has already decoded, and derives the very
// probabilities used to read the mode from how those neighbours voted. An
// encoder that picks its own values does not produce a differently-compressed
// stream; it produces a stream the decoder reads as something else entirely,
// and the arithmetic decoder never recovers.
//
// So everything here is a reimplementation, not a design: the encoder's job is
// to arrive at exactly the numbers the decoder will arrive at.

// mvRefCount indexes the four accumulators that vp8_find_near_mvs fills, in the
// order the format defines them.
const (
	cntIntra = iota
	cntNearest
	cntNear
	cntSplit
)

// modeContexts maps the neighbour vote counts to the probabilities used to read
// the inter prediction mode. The decoder computes the same counts from the same
// neighbours and indexes the same table, which is why the encoder cannot
// substitute a static table of its own.
//
// Reference: RFC 6386 §16.3, vp8_mode_contexts.
var modeContexts = [6][4]uint8{
	{7, 1, 1, 143},
	{14, 18, 14, 107},
	{135, 64, 57, 68},
	{60, 56, 128, 65},
	{159, 134, 128, 34},
	{234, 188, 128, 28},
}

// nearMVs carries what the predictor derivation produces for one macroblock.
type nearMVs struct {
	// nearest and near are the two candidate motion vectors, clamped.
	nearest, near motionVector
	// best is the vector a NEWMV delta is coded against. It is not simply
	// `nearest`: when no neighbour outvotes the intra count it stays zero.
	best motionVector
	// counts are the neighbour votes, in cntIntra..cntSplit order.
	counts [4]int
}

// probs returns the four probabilities the inter mode tree is coded with.
//
// Reference: RFC 6386 §16.3, vp8_mv_ref_probs.
func (n nearMVs) probs() [4]uint8 {
	var p [4]uint8
	for i := 0; i < 4; i++ {
		c := n.counts[i]
		if c > 5 {
			c = 5
		}
		p[i] = modeContexts[c][i]
	}
	return p
}

// neighbourMV describes one already-coded neighbour, as the decoder sees it.
type neighbourMV struct {
	// inter is false for an intra macroblock and for anything outside the
	// frame. The decoder's mode-info array carries a zeroed border row and
	// column, and a zeroed entry reads as intra — so out-of-frame neighbours
	// and intra neighbours are the same case, not two.
	inter bool
	mv    motionVector
	split bool
}

// findNearMVs derives the motion-vector predictors and the neighbour vote
// counts for one macroblock, from the above, left and above-left neighbours.
//
// This replaces a weighted-candidate scheme that counted the above-RIGHT
// neighbour and picked the two most frequent vectors. That scheme is a
// reasonable way to guess a good predictor and a wrong way to agree with a
// decoder, which is the only thing that matters here.
//
// Reference: RFC 6386 §16.3, vp8_find_near_mvs.
func findNearMVs(above, left, aboveLeft neighbourMV, mbX, mbY, mbW, mbH int) nearMVs {
	// mvs[0] doubles as the "best" slot, exactly as in the reference: the
	// derivation writes candidates from index 1 upward and may later copy the
	// nearest candidate down into slot 0.
	var mvs [4]motionVector
	var counts [4]int

	// slot and bucket walk forward together. A neighbour with a non-zero
	// vector distinct from the previous one opens a new slot; a neighbour that
	// agrees adds its weight to the slot already open.
	slot, bucket := 0, 0

	if above.inter {
		if above.mv != zeroMV {
			slot++
			mvs[slot] = above.mv
			bucket++
		}
		counts[bucket] += 2
	}

	if left.inter {
		if left.mv != zeroMV {
			if left.mv != mvs[slot] {
				slot++
				mvs[slot] = left.mv
				bucket++
			}
			counts[bucket] += 2
		} else {
			counts[cntIntra] += 2
		}
	}

	if aboveLeft.inter {
		if aboveLeft.mv != zeroMV {
			if aboveLeft.mv != mvs[slot] {
				slot++
				mvs[slot] = aboveLeft.mv
				bucket++
			}
			counts[bucket]++
		} else {
			counts[cntIntra]++
		}
	}

	// Three distinct vectors where the third repeats the first: the first
	// absorbs the vote rather than the third keeping its own.
	if counts[cntSplit] > 0 && mvs[slot] == mvs[cntNearest] {
		counts[cntNearest]++
	}

	// The split bucket stops being a vector count here and becomes a count of
	// neighbours coded as SPLITMV. This encoder never emits SPLITMV, so it is
	// always zero — but it is derived rather than assumed, because a future
	// SPLITMV would otherwise silently desynchronise this.
	counts[cntSplit] = 0
	if above.split {
		counts[cntSplit] += 2
	}
	if left.split {
		counts[cntSplit] += 2
	}
	if aboveLeft.split {
		counts[cntSplit]++
	}

	if counts[cntNear] > counts[cntNearest] {
		counts[cntNearest], counts[cntNear] = counts[cntNear], counts[cntNearest]
		mvs[cntNearest], mvs[cntNear] = mvs[cntNear], mvs[cntNearest]
	}

	if counts[cntNearest] >= counts[cntIntra] {
		mvs[cntIntra] = mvs[cntNearest]
	}

	return nearMVs{
		nearest: clampMVToFrame(mvs[cntNearest], mbX, mbY, mbW, mbH),
		near:    clampMVToFrame(mvs[cntNear], mbX, mbY, mbW, mbH),
		best:    clampMVToFrame(mvs[cntIntra], mbX, mbY, mbW, mbH),
		counts:  counts,
	}
}

// mvFrameMarginQPel is how far outside the frame a predictor may point: one
// macroblock, in quarter-pel units.
//
// The reference expresses this in eighth-pel units, because that is how it
// stores motion vectors; this encoder stores quarter-pel, so every bound here
// is half of the reference's. The two agree on pixels, which is what the
// decoder actually compares.
const mvFrameMarginQPel = 16 * 4

// clampMVToFrame keeps a predictor within one macroblock of the frame.
//
// Reference: RFC 6386 §16.3, vp8_clamp_mv2.
func clampMVToFrame(mv motionVector, mbX, mbY, mbW, mbH int) motionVector {
	toLeft := int16(-mbX*16*4 - mvFrameMarginQPel)
	toRight := int16((mbW-1-mbX)*16*4 + mvFrameMarginQPel)
	toTop := int16(-mbY*16*4 - mvFrameMarginQPel)
	toBottom := int16((mbH-1-mbY)*16*4 + mvFrameMarginQPel)

	if mv.dx < toLeft {
		mv.dx = toLeft
	} else if mv.dx > toRight {
		mv.dx = toRight
	}
	if mv.dy < toTop {
		mv.dy = toTop
	} else if mv.dy > toBottom {
		mv.dy = toBottom
	}
	return mv
}

// collectNeighbours reads the three neighbours the predictor derivation needs.
// Anything outside the frame reads as intra, which is what the decoder's zeroed
// border entries amount to.
func collectNeighbours(mbs []macroblock, mbX, mbY, mbW int) (above, left, aboveLeft neighbourMV) {
	at := func(x, y int) neighbourMV {
		if x < 0 || y < 0 || x >= mbW {
			return neighbourMV{}
		}
		mb := &mbs[y*mbW+x]
		if !mb.isInter {
			return neighbourMV{}
		}
		return neighbourMV{inter: true, mv: mb.mv}
	}
	return at(mbX, mbY-1), at(mbX-1, mbY), at(mbX-1, mbY-1)
}

// interYModeProb is the luma mode probability row for an intra macroblock
// inside an inter frame.
//
// Reference: RFC 6386 §11.3, vp8_ymode_prob.
var interYModeProb = [4]uint8{112, 86, 140, 37}

// interUVModeProb is the chroma mode probability row for an intra macroblock
// inside an inter frame.
//
// Reference: RFC 6386 §11.3, vp8_uv_mode_prob.
var interUVModeProb = [3]uint8{162, 101, 204}

// interBModeProb is the sub-block mode probability row for B_PRED inside an
// inter frame. Unlike the key-frame case it carries no above/left context: one
// row serves every sub-block.
//
// Reference: RFC 6386 §11.3, vp8_bmode_prob.
var interBModeProb = [9]uint8{120, 90, 79, 133, 87, 85, 80, 111, 151}

// encodeInterYMode encodes the luma mode of an intra macroblock inside an inter
// frame.
//
// The tree is not the key frame's. A key frame splits B_PRED off first and
// codes the four 16x16 modes below it; an inter frame splits DC_PRED off first
// and pairs {V,H} against {TM,B_PRED}. Coding one with the other's shape
// desynchronises the decoder even when every probability is right.
//
// Reference: RFC 6386 §11.2, vp8_ymode_tree.
func encodeInterYMode(enc *boolEncoder, mode intraMode) {
	p := interYModeProb
	if mode == DC_PRED {
		enc.putBit(p[0], false)
		return
	}
	enc.putBit(p[0], true)

	switch mode {
	case V_PRED:
		enc.putBit(p[1], false)
		enc.putBit(p[2], false)
	case H_PRED:
		enc.putBit(p[1], false)
		enc.putBit(p[2], true)
	case TM_PRED:
		enc.putBit(p[1], true)
		enc.putBit(p[3], false)
	default: // B_PRED
		enc.putBit(p[1], true)
		enc.putBit(p[3], true)
	}
}

// encodeInterMBMode encodes one macroblock's mode within an inter frame,
// including the motion vector when the mode carries one.
//
// probIntra, probLast and probGolden are the values signalled in the frame
// header; they are passed in rather than hardcoded so that the header and the
// macroblock layer cannot drift apart.
func encodeInterMBMode(enc *boolEncoder, mb *macroblock, near nearMVs, probIntra, probLast, probGolden uint8) {
	if !mb.isInter {
		enc.putBit(probIntra, false)
		encodeInterYMode(enc, mb.lumaMode)
		if mb.lumaMode == B_PRED {
			for _, bm := range mb.bModes {
				encodeBModeWithProbs(enc, bm, interBModeProb)
			}
		}
		encodeUVModeWithProbs(enc, mb.chromaMode, interUVModeProb)
		return
	}

	enc.putBit(probIntra, true)

	switch mb.refFrame {
	case refFrameGolden:
		enc.putBit(probLast, true)
		enc.putBit(probGolden, false)
	case refFrameAltRef:
		enc.putBit(probLast, true)
		enc.putBit(probGolden, true)
	default: // refFrameLast
		enc.putBit(probLast, false)
	}

	// The mode tree branches on ZEROMV first, not on NEARESTMV, and carries
	// five symbols rather than four -- SPLITMV is the fifth, and its absence
	// from the tree does not make the tree shorter.
	//
	// Reference: RFC 6386 §16.2, vp8_mv_ref_tree.
	p := near.probs()
	switch mb.interMode {
	case mvModeZeroMV:
		enc.putBit(p[0], false)
	case mvModeNearestMV:
		enc.putBit(p[0], true)
		enc.putBit(p[1], false)
	case mvModeNearMV:
		enc.putBit(p[0], true)
		enc.putBit(p[1], true)
		enc.putBit(p[2], false)
	default: // mvModeNewMV
		enc.putBit(p[0], true)
		enc.putBit(p[1], true)
		enc.putBit(p[2], true)
		enc.putBit(p[3], false)
		encodeMV(enc, mb.mv, near.best)
	}
}
