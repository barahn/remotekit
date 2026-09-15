package vp8

// This file implements VP8 inter-frame (P-frame) bitstream encoding.
// It handles:
//   - Inter-frame header encoding (different from key-frame header)
//   - Motion vector encoding
//   - Inter prediction mode encoding
//   - Frame assembly for inter frames
//
// Reference: RFC 6386 §9.2 (inter frame header), §16 (inter prediction),
//            §17 (motion vector encoding)

// interMVProbs contains the VP8 default motion vector component probabilities.
//
// The first row belongs to the vertical component and the second to the
// horizontal one -- in that order, because that is the order the decoder reads
// them in. They were previously labelled the other way round and used the other
// way round, which coded every motion vector's rows with the columns' table.
//
// Reference: RFC 6386 §17.2, vp8_default_mv_context
var interMVProbs = [2][19]uint8{
	// Row (vertical, dy) component probabilities
	{
		162,               // is_short
		128,               // sign
		225,               // short tree bit 0
		146,               // short tree bit 1
		172,               // short tree bit 2
		147, 214, 39, 156, // short tree bits 3..6
		128, 129, 132, 75, 145, 178, 206, 239, 254, 254, // long bits
	},
	// Column (horizontal, dx) component probabilities
	{
		164,                // is_short
		128,                // sign
		204,                // short tree bit 0
		170,                // short tree bit 1
		119,                // short tree bit 2
		235, 140, 230, 228, // short tree bits 3..6
		128, 130, 130, 74, 148, 180, 203, 236, 254, 254, // long bits
	},
}

// encodeMVComponent encodes a single motion vector component (dx or dy)
// using the VP8 motion vector coding scheme per RFC 6386 §17.1.
// The component is a signed value in quarter-pixel units.
//
// Probability table offsets:
//   - probs[0]: mvpis_short (short vs long)
//   - probs[1]: sign
//   - probs[2..8]: short tree (7 probabilities for 8 values)
//   - probs[9..18]: long bits (10 probabilities for bits 0-9)
func encodeMVComponent(enc *boolEncoder, val int16, probs [19]uint8) {
	sign := val < 0
	absVal := int(val)
	if sign {
		absVal = -absVal
	}

	// Clamp absVal to valid range (10 bits max, 0-1023)
	if absVal > 1023 {
		absVal = 1023
	}

	if absVal < 8 {
		// Short encoding (magnitude 0..7): use 7-node tree at probs[2..8]
		enc.putBit(probs[0], false) // is_short = false (short)
		encodeSmallMV(enc, absVal, probs)
	} else {
		// Long encoding (magnitude >= 8)
		enc.putBit(probs[0], true) // is_short = true (long)
		encodeLargeMV(enc, absVal, probs)
	}

	// Encode sign if value is non-zero
	if absVal > 0 {
		enc.putBit(probs[1], sign)
	}
}

// encodeSmallMV encodes MV magnitude 0-7 using the RFC 6386 §17.1 small_mvtree.
// Tree structure (probs[2..8]):
//
//	       [0]
//	      /   \
//	    [1]   [4]
//	   /   \  /   \
//	 [2]  [3][5]  [6]
//	 /\   /\  /\   /\
//	0  1 2  3 4  5 6  7
//
// Node indices: 0->probs[2], 1->probs[3], 2->probs[4], 3->probs[5],
//
//	4->probs[6], 5->probs[7], 6->probs[8]
func encodeSmallMV(enc *boolEncoder, v int, probs [19]uint8) {
	// First split: {0,1,2,3} vs {4,5,6,7}
	enc.putBit(probs[2], v >= 4)
	if v >= 4 {
		// Right subtree {4,5,6,7}
		v -= 4
		// Split: {4,5} vs {6,7}
		enc.putBit(probs[6], v >= 2)
		if v >= 2 {
			// {6,7}
			enc.putBit(probs[8], v == 3) // 6 or 7
		} else {
			// {4,5}
			enc.putBit(probs[7], v == 1) // 4 or 5
		}
	} else {
		// Left subtree {0,1,2,3}
		// Split: {0,1} vs {2,3}
		enc.putBit(probs[3], v >= 2)
		if v >= 2 {
			// {2,3}
			enc.putBit(probs[5], v == 3) // 2 or 3
		} else {
			// {0,1}
			enc.putBit(probs[4], v == 1) // 0 or 1
		}
	}
}

// encodeLargeMV encodes MV magnitude >= 8 per RFC 6386 §17.1.
// For long values, bits are encoded in a specific order:
//   - Bits 0, 1, 2 (using probs[9], probs[10], probs[11])
//   - Bits 9, 8, 7, 6, 5, 4 (using probs[18], probs[17], probs[16], probs[15], probs[14], probs[13])
//   - Bit 3 is conditionally encoded (using probs[12]) only if needed
//
// Since the value is >= 8, if the high bits (bits 4-9) are all zero, bit 3 must
// be 1 and is not explicitly coded.
func encodeLargeMV(enc *boolEncoder, v int, probs [19]uint8) {
	// Encode bits 0, 1, 2 (LSBs)
	for i := 0; i < 3; i++ {
		bit := (v >> i) & 1
		enc.putBit(probs[9+i], bit == 1)
	}

	// Encode bits 9, 8, 7, 6, 5, 4 (high bits, descending order)
	for i := 9; i >= 4; i-- {
		bit := (v >> i) & 1
		enc.putBit(probs[9+i], bit == 1)
	}

	// Bit 3: only encode if high bits (4-9) are non-zero
	// If v <= 15 (only bits 0-3 set) and v >= 8, then bit 3 must be 1
	// and is implicitly known to the decoder
	if v&0xFFF0 != 0 {
		// High bits are non-zero, so bit 3 must be explicitly coded
		bit := (v >> 3) & 1
		enc.putBit(probs[12], bit == 1)
	}
	// If high bits are zero, bit 3 is implicitly 1 (since v >= 8)
}

// encodeMV encodes a full motion vector (dx, dy) as the difference from
// the predicted motion vector.
func encodeMV(enc *boolEncoder, mv, predMV motionVector) {
	// Row before column. The decoder reads the vertical component first, so
	// writing the horizontal one first hands it a vector with its axes
	// exchanged -- and, since the two components use different probability
	// tables, a different number of bits as well.
	encodeMVComponent(enc, mv.dy-predMV.dy, interMVProbs[0])
	encodeMVComponent(enc, mv.dx-predMV.dx, interMVProbs[1])
}

// encodeInterFrameHeader encodes the VP8 inter-frame (P-frame) header into
// the first partition. Inter-frame headers differ from key-frame headers in
// several ways per RFC 6386 §9.
func encodeInterFrameHeader(enc *boolEncoder, width, height, qi int, deltas QuantDeltas,
	partCount PartitionCount, loopFilter loopFilterParams, refreshGolden bool, mbs []macroblock,
) {
	encodeInterFrameHeaderWithProbs(enc, width, height, qi, deltas, partCount, loopFilter, refreshGolden, mbs, nil)
}

// encodeInterFrameHeaderWithProbs encodes the inter-frame header with optional probability updates.
func encodeInterFrameHeaderWithProbs(enc *boolEncoder, width, height, qi int, deltas QuantDeltas,
	partCount PartitionCount, loopFilter loopFilterParams, refreshGolden bool, mbs []macroblock, probCfg *ProbConfig,
) {
	encodeCommonFrameHeader(enc, qi, deltas, partCount, loopFilter)
	encodeRefFrameFlags(enc, refreshGolden)
	encodeProbUpdates(enc, probCfg)
	probs := measureFrameProbs(mbs)
	encodeInterFrameProbs(enc, probs)
	encodeIntraModeProbUpdates(enc)
	encodeMVProbUpdates(enc)
	encodeInterMBModes(enc, width, height, mbs, probs)
}

// encodeRefFrameFlags encodes reference frame refresh and copy flags.
func encodeRefFrameFlags(enc *boolEncoder, refreshGolden bool) {
	enc.putBit(128, refreshGolden) // refresh_golden_frame
	enc.putBit(128, false)         // refresh_alternate_frame
	enc.putLiteral(0, 2)           // copy_buffer_to_golden
	enc.putLiteral(0, 2)           // copy_buffer_to_alternate
	enc.putBit(128, false)         // sign_bias_golden
	enc.putBit(128, false)         // sign_bias_alternate
	enc.putBit(128, false)         // refresh_entropy_probs
	enc.putBit(128, true)          // refresh_last_frame_buffer
}

// encodeProbUpdates encodes token probability updates if configured.
func encodeProbUpdates(enc *boolEncoder, probCfg *ProbConfig) {
	if probCfg != nil && probCfg.NewProbs != nil && probCfg.CurrentProbs != nil {
		EncodeCoeffProbUpdates(enc, probCfg.CurrentProbs, probCfg.NewProbs)
	} else {
		EncodeNoCoeffProbUpdates(enc)
	}
}

// The four probabilities the inter header signals and the macroblock layer then
// has to code with. They live here as constants so that the header and the
// macroblock layer cannot drift apart: a value changed in one place and not the
// other desynchronises the decoder without changing anything the encoder can
// see for itself.
// frameProbs are the per-frame probabilities the inter header signals and the
// macroblock layer then codes with. They are measured from the frame rather
// than fixed, for the same reason prob_skip_false is: a probability that does
// not describe the frame does not merely compress worse, it prices the common
// case at its worst. With every macroblock inter-coded against the last
// reference -- the normal state of a screen stream -- fixed values spent about
// 1.4 bits per macroblock saying so, which is 200 bytes a frame at 640x480
// before any picture content is coded at all.
type frameProbs struct {
	skip   uint8
	intra  uint8
	last   uint8
	golden uint8
}

// measureFrameProbs derives the header probabilities from the macroblocks the
// frame actually contains.
//
// Each is the probability, out of 256, that the corresponding flag reads as
// zero: prob_intra that a macroblock is intra, prob_last that an inter
// macroblock references the last frame, prob_golden that a non-last reference
// is golden rather than altref. All are clamped away from 0 and 256, since
// either extreme makes the opposite case unencodable.
func measureFrameProbs(mbs []macroblock) frameProbs {
	p := frameProbs{skip: skipProbability(mbs), intra: 128, last: 128, golden: 128}
	if len(mbs) == 0 {
		return p
	}

	intra, inter, last, golden := 0, 0, 0, 0
	for i := range mbs {
		if !mbs[i].isInter {
			intra++
			continue
		}
		inter++
		switch mbs[i].refFrame {
		case refFrameLast:
			last++
		case refFrameGolden:
			golden++
		}
	}

	p.intra = clampProb(uint32(intra * 256 / len(mbs)))
	if inter > 0 {
		p.last = clampProb(uint32(last * 256 / inter))
		if notLast := inter - last; notLast > 0 {
			p.golden = clampProb(uint32(golden * 256 / notLast))
		}
	}
	return p
}

// encodeInterFrameProbs encodes the inter-frame specific probability values.
func encodeInterFrameProbs(enc *boolEncoder, p frameProbs) {
	enc.putBit(128, true) // mb_no_skip_coeff
	enc.putLiteral(uint32(p.skip), 8)
	enc.putLiteral(uint32(p.intra), 8)
	enc.putLiteral(uint32(p.last), 8)
	enc.putLiteral(uint32(p.golden), 8)
}

// mvUpdateProbs are the probabilities with which each motion-vector
// probability-update flag is coded. They are fixed by the format, not chosen by
// the encoder: the decoder reads these flags with exactly these values, so
// writing them with any other probability desynchronises the arithmetic decoder
// for the rest of the first partition — which is where every macroblock mode
// and motion vector lives.
//
// Reference: RFC 6386 §17.2, vp8_mv_update_probs.
var mvUpdateProbs = [2][19]uint8{
	{
		237, 246, 253, 253, 254, 254, 254, 254, 254,
		254, 254, 254, 254, 254, 250, 250, 252, 254, 254,
	},
	{
		231, 243, 245, 253, 254, 254, 254, 254, 254,
		254, 254, 254, 254, 254, 251, 251, 254, 254, 254,
	},
}

// encodeIntraModeProbUpdates signals that the intra mode probabilities are not
// being updated. Both flags are mandatory in an inter frame header; omitting
// them leaves the decoder two bits ahead of the encoder.
//
// Reference: RFC 6386 §9.11.
func encodeIntraModeProbUpdates(enc *boolEncoder) {
	enc.putBit(128, false) // intra_16x16_prob_update_flag
	enc.putBit(128, false) // intra_chroma_prob_update_flag
}

// encodeMVProbUpdates signals no MV probability updates.
func encodeMVProbUpdates(enc *boolEncoder) {
	for i := 0; i < 2; i++ {
		for j := 0; j < 19; j++ {
			enc.putBit(mvUpdateProbs[i][j], false)
		}
	}
}

// encodeInterMBModes encodes macroblock modes for an inter frame.
//
// Every macroblock's mode is coded with probabilities derived from its already
// coded neighbours, so this walks the frame in the same raster order the
// decoder does and derives the same predictors from the same three neighbours.
func encodeInterMBModes(enc *boolEncoder, width, height int, mbs []macroblock, probs frameProbs) {
	mbW := (width + 15) / 16
	mbH := (height + 15) / 16

	for mbIdx := range mbs {
		mb := &mbs[mbIdx]
		mbX := mbIdx % mbW
		mbY := mbIdx / mbW

		enc.putBit(probs.skip, mb.skip)

		above, left, aboveLeft := collectNeighbours(mbs, mbX, mbY, mbW)
		near := findNearMVs(above, left, aboveLeft, mbX, mbY, mbW, mbH)
		encodeInterMBMode(enc, mb, near, probs.intra, probs.last, probs.golden)
	}
}

// BuildInterFrame constructs a complete VP8 inter-frame (P-frame) bitstream.
// Inter frames use a frame tag with key_frame=1 (inter) and do not include
// the start code or dimensions.
//
// If refreshGolden is true, the bitstream signals that the decoder should
// update its golden reference frame from the reconstructed frame.
//
// Reference: RFC 6386 §9.1 (frame tag for inter frames)
func BuildInterFrame(width, height, qi, y1DCDelta, y2DCDelta, y2ACDelta, uvDCDelta, uvACDelta int,
	partCount PartitionCount, loopFilter loopFilterParams, refreshGolden bool, mbs []macroblock,
) ([]byte, error) {
	return buildInterFrameWithProbs(width, height, qi, y1DCDelta, y2DCDelta, y2ACDelta, uvDCDelta, uvACDelta,
		partCount, loopFilter, refreshGolden, mbs, nil)
}

// buildInterFrameWithProbs constructs an inter frame with optional probability configuration.
func buildInterFrameWithProbs(width, height, qi, y1DCDelta, y2DCDelta, y2ACDelta, uvDCDelta, uvACDelta int,
	partCount PartitionCount, loopFilter loopFilterParams, refreshGolden bool, mbs []macroblock, probCfg *ProbConfig,
) ([]byte, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, errInvalidDimensions
	}

	mbW := (width + 15) / 16
	mbH := (height + 15) / 16

	deltas := QuantDeltas{
		Y1DC: y1DCDelta,
		Y2DC: y2DCDelta,
		Y2AC: y2ACDelta,
		UVDC: uvDCDelta,
		UVAC: uvACDelta,
	}

	// Encode first partition (inter-frame header + MB modes)
	partEnc := newBoolEncoder()
	encodeInterFrameHeaderWithProbs(partEnc, width, height, qi, deltas, partCount, loopFilter, refreshGolden, mbs, probCfg)
	firstPart := partEnc.flush()

	// Encode residual partitions using shared helper from bitstream.go
	residualParts := encodeResidualPartitionsWithProbs(partCount, mbs, mbW, mbH, probCfg)

	// Build inter frame using shared assembler
	return assembleInterFrameBitstream(firstPart, residualParts)
}
