// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
// Forked from github.com/opd-ai/vp8 (MIT); see LICENSE.upstream.

// Package vp8 provides a pure-Go VP8 encoder supporting both key frames
// (I-frames) and inter frames (P-frames) with motion estimation.
//
// The encoder produces VP8 bitstreams per RFC 6386, compatible with
// WebRTC stacks (pion/rtp VP8Payloader, ivfwriter).
//
// In the default mode (keyFrameInterval=0), every Encode call produces a
// key frame for backward compatibility. When SetKeyFrameInterval is called
// with a positive value, the encoder produces inter frames using motion
// estimation from reference frames between key frames.
//
// Limitations:
//   - No sub-pixel motion estimation (integer-pel only).
//   - No segmentation, no temporal scalability.
//
// Usage (I-frame only):
//
//	enc, err := vp8.NewEncoder(640, 480, 30)
//	if err != nil { ... }
//	vp8Bytes, err := enc.Encode(yuvFrame)
//
// Usage (inter-frame):
//
//	enc, err := vp8.NewEncoder(640, 480, 30)
//	if err != nil { ... }
//	enc.SetKeyFrameInterval(30)
//	enc.SetLoopFilterLevel(20)
//	for _, yuv := range frames {
//	    vp8Bytes, err := enc.Encode(yuv)
//	}
package vp8

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidDimensions       = errors.New("width and height must be positive and even")
	ErrInvalidPartitionCount   = errors.New("partition count must be 0-3 (OnePartition through EightPartitions)")
	ErrInvalidQuantizerIndex   = errors.New("quantizer index must be 0-127")
	ErrInvalidBitrate          = errors.New("bitrate must be positive")
	ErrInvalidKeyFrameInterval = errors.New("key frame interval must be non-negative")
	ErrInvalidLoopFilterLevel  = errors.New("loop filter level must be 0-63")
)

// Encoder encodes raw YUV420 frames into VP8 key-frame bitstreams.
type Encoder struct {
	width   int
	height  int
	fps     int
	bitrate int // target bitrate in bits/s (used to derive quantizer)
	qi      int // quantizer index [0, 127]

	// Quantizer delta fields for per-plane adjustments.
	// These are added to the base qi for specific coefficient types.
	y1DCDelta int // Y1 DC coefficient delta
	y2DCDelta int // Y2 (WHT DC-of-DC) DC coefficient delta
	y2ACDelta int // Y2 AC coefficient delta
	uvDCDelta int // Chroma DC coefficient delta
	uvACDelta int // Chroma AC coefficient delta

	// partitionCount controls the number of DCT/residual partitions.
	// Default is OnePartition. Use SetPartitionCount to enable multi-partition encoding.
	partitionCount PartitionCount

	// Inter-frame encoding state.
	// refFrames manages the three reference frame buffers (last, golden, altref).
	refFrames *refFrameManager
	// frameCount tracks the number of frames encoded since the last key frame.
	frameCount int
	// keyFrameInterval is the maximum number of frames between key frames.
	// 0 means every frame is a key frame (I-frame only mode).
	// Default is 0 for backward compatibility.
	keyFrameInterval int
	// forceNextKeyFrame forces the next Encode call to produce a key frame.
	forceNextKeyFrame bool
	// loopFilter controls the loop filter parameters.
	loopFilter loopFilterParams

	// Golden frame management
	// goldenFrameInterval is the number of frames between golden frame updates.
	// 0 means no automatic golden updates (only last frame is updated).
	goldenFrameInterval int
	// forceNextGolden forces the next inter frame to copy last→golden.
	forceNextGolden bool
	// goldenFrameCount tracks frames since last golden update.
	goldenFrameCount int

	// Coefficient probability adaptation
	// coeffHistogram tracks token statistics for probability updates.
	coeffHistogram *CoeffHistogram
	// coeffProbs stores the current coefficient probabilities (may differ from defaults).
	coeffProbs [4][8][3][11]uint8
	// useProbUpdates enables adaptive probability updates when beneficial.
	useProbUpdates bool

	// screenContent restricts inter macroblocks to ZEROMV, skip and intra.
	// See SetScreenContentProfile.
	screenContent bool
	// dirtyMap marks which macroblocks changed since the previous frame, in
	// raster order. It is advisory input from the caller, consumed by the next
	// Encode and then cleared.
	dirtyMap []bool
}

// NewEncoder creates a new VP8 Encoder for frames of the given dimensions
// and frame rate. Both width and height must be positive even integers.
//
// fps is used for bitrate-to-quantizer mapping and must be > 0.
func NewEncoder(width, height, fps int) (*Encoder, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("vp8: width and height must be positive, got %dx%d", width, height)
	}
	if width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("vp8: width and height must be even, got %dx%d", width, height)
	}
	if fps <= 0 {
		return nil, errors.New("vp8: fps must be positive")
	}
	enc := &Encoder{
		width:          width,
		height:         height,
		fps:            fps,
		bitrate:        500_000, // default 500 kbps
		qi:             24,      // default quantizer index
		refFrames:      newRefFrameManager(width, height),
		coeffHistogram: NewCoeffHistogram(),
		coeffProbs:     DefaultCoeffProbs,
	}
	return enc, nil
}

// SetBitrate configures the target output bitrate in bits per second.
// The encoder maps this to a VP8 quantizer index. Values outside the
// typical WebRTC range (100 kbps – 8 Mbps) are clamped.
func (e *Encoder) SetBitrate(bitrate int) {
	if bitrate < 100_000 {
		bitrate = 100_000
	}
	if bitrate > 8_000_000 {
		bitrate = 8_000_000
	}
	e.bitrate = bitrate
	// Rough linear mapping: higher bitrate → lower QI (better quality).
	// qi ∈ [4, 63]: 8 Mbps → qi=4, 100 kbps → qi=63.
	ratio := float64(e.bitrate-100_000) / float64(8_000_000-100_000)
	e.qi = 63 - int(ratio*59)
}

// ForceKeyFrame causes the next call to Encode to produce a key frame.
// This resets the inter-frame prediction chain.
func (e *Encoder) ForceKeyFrame() {
	e.forceNextKeyFrame = true
}

// SetKeyFrameInterval configures the maximum number of inter frames between
// key frames. A value of 0 means every frame is a key frame (I-frame only mode,
// which is the default for backward compatibility). A value of N means that
// every Nth frame will be a key frame, with inter frames in between.
//
// For example, SetKeyFrameInterval(30) at 30fps means one key frame per second.
func (e *Encoder) SetKeyFrameInterval(interval int) {
	if interval < 0 {
		interval = 0
	}
	e.keyFrameInterval = interval
}

// SetLoopFilterLevel configures the loop filter strength (0–63).
//
// Do not raise it above 0 yet. The header signals the normal filter while the
// encoder applies a simplified one to its own reference, so a non-zero level
// makes the two reconstructions diverge and the divergence compounds. The
// default is 0, where both sides filter nothing and therefore agree. This is
// the one inherited defect the conformance tests do not currently cover,
// because they exercise the default.
// The loop filter reduces blocking artifacts in reconstructed frames used
// as reference for inter-frame prediction. Level 0 disables the filter.
// A moderate level (e.g., 20–40) is recommended for inter-frame encoding.
func (e *Encoder) SetLoopFilterLevel(level int) {
	if level < 0 {
		level = 0
	}
	if level > 63 {
		level = 63
	}
	e.loopFilter.level = level
}

// SetGoldenFrameInterval configures the interval for golden frame updates.
// A value of 0 (default) means no automatic golden updates — only the last
// frame is updated after each encode. A value of N means the last frame is
// copied to golden every N inter frames.
//
// Golden frames provide a longer-term reference for motion compensation,
// improving quality after scene cuts or when the last frame has drifted.
//
// For example, SetGoldenFrameInterval(10) with SetKeyFrameInterval(30)
// means: key frame, 9 inter frames, golden update, 9 inter frames,
// golden update, 9 inter frames, key frame, etc.
func (e *Encoder) SetGoldenFrameInterval(interval int) {
	if interval < 0 {
		interval = 0
	}
	e.goldenFrameInterval = interval
}

// ForceGoldenFrame causes the next inter frame to update the golden reference
// frame by copying the reconstructed last frame to golden. This is useful for
// manual scene-cut detection or periodic quality anchoring.
//
// For key frames, this has no effect since golden is always updated from key.
func (e *Encoder) ForceGoldenFrame() {
	e.forceNextGolden = true
}

// SetPartitionCount configures the number of DCT/residual partitions.
// VP8 supports 1, 2, 4, or 8 partitions. Multiple partitions can enable
// parallel decoding and provide error resilience.
//
// Valid values: OnePartition, TwoPartitions, FourPartitions, EightPartitions.
// Default is OnePartition.
//
// Returns ErrInvalidPartitionCount if count is not one of the valid values.
func (e *Encoder) SetPartitionCount(count PartitionCount) error {
	if count < OnePartition || count > EightPartitions {
		return ErrInvalidPartitionCount
	}
	e.partitionCount = count
	return nil
}

// SetProbabilityUpdates enables or disables adaptive coefficient probability updates.
// When enabled, the encoder tracks token statistics and updates the probability tables
// in frame headers when doing so improves compression efficiency.
//
// This can improve compression for content with consistent coefficient distributions,
// at the cost of slightly increased header size. The encoder automatically decides
// whether to emit updates based on estimated bit savings.
//
// Default is false (disabled) for backward compatibility.
func (e *Encoder) SetProbabilityUpdates(enabled bool) {
	e.useProbUpdates = enabled
}

// SetQuantizerDeltas configures per-plane quantizer delta values.
// These deltas are added to the base quantizer index (qi) for specific
// coefficient types, allowing fine-tuned quality trade-offs.
//
// Parameters:
//   - y1dc: delta for Y1 (luma 4x4 block) DC coefficients
//   - y2dc: delta for Y2 (WHT DC-of-DC) DC coefficients
//   - y2ac: delta for Y2 AC coefficients
//   - uvdc: delta for chroma DC coefficients
//   - uvac: delta for chroma AC coefficients
//
// Positive deltas increase quantization (lower quality, smaller size).
// Negative deltas decrease quantization (higher quality, larger size).
// Deltas are clamped so that qi+delta stays within [0, 127].
func (e *Encoder) SetQuantizerDeltas(y1dc, y2dc, y2ac, uvdc, uvac int) {
	e.y1DCDelta = y1dc
	e.y2DCDelta = y2dc
	e.y2ACDelta = y2ac
	e.uvDCDelta = uvdc
	e.uvACDelta = uvac
}

// Encode encodes a raw YUV420 (I420) frame and returns a VP8 bitstream.
// Depending on the configuration, this may produce either a key frame (I-frame)
// or an inter frame (P-frame) using motion estimation from reference frames.
//
// When keyFrameInterval is 0 (default), every frame is a key frame.
// When keyFrameInterval > 0, inter frames are produced between key frames.
//
// The returned bytes can be passed directly to pion/rtp's VP8Payloader.Payload.
func (e *Encoder) Encode(yuv []byte) ([]byte, error) {
	frame, err := NewYUV420Frame(yuv, e.width, e.height)
	if err != nil {
		return nil, err
	}

	isKeyFrame := e.shouldEncodeKeyFrame()
	qf := GetQuantFactors(e.qi, e.y1DCDelta, e.y2DCDelta, e.y2ACDelta, e.uvDCDelta, e.uvACDelta)
	mbs, recon := e.processAllMacroblocks(frame, isKeyFrame, qf)
	// The map describes one frame only. Keeping it would apply last frame's
	// notion of what changed to the next one.
	e.dirtyMap = nil

	if isKeyFrame || !e.refFrames.hasReference(refFrameLast) {
		return e.encodeKeyFrame(mbs, recon)
	}
	return e.encodeInterFrame(mbs, recon)
}

// processAllMacroblocks processes all macroblocks in the frame.
func (e *Encoder) processAllMacroblocks(frame *Frame, isKeyFrame bool, qf QuantFactors) ([]macroblock, refFrameBuffer) {
	mbW := (e.width + 15) / 16
	mbH := (e.height + 15) / 16
	chromaW := e.width / 2
	chromaH := e.height / 2
	mbs := make([]macroblock, mbW*mbH)

	// The reconstruction is built here, macroblock by macroblock, rather than
	// in a second pass. Prediction has to see the pixels a decoder will see:
	// residuals computed against the source frame's neighbours cannot correct
	// an error they were never told about, and the error then compounds along
	// the row. Closing that loop means a macroblock cannot be analysed until
	// its neighbours are reconstructed, which is why this is serial.
	recon := e.refFrames.allocBuffer()
	recon.valid = true

	if isKeyFrame || !e.refFrames.hasReference(refFrameLast) {
		e.processKeyFrameMBs(frame, mbs, &recon, mbW, mbH, chromaW, chromaH, qf)
	} else {
		e.processInterFrameMBs(frame, mbs, &recon, mbW, mbH, chromaW, chromaH, qf)
	}
	return mbs, recon
}

// processKeyFrameMBs analyses and reconstructs macroblocks for a key frame.
func (e *Encoder) processKeyFrameMBs(frame *Frame, mbs []macroblock, recon *refFrameBuffer, mbW, mbH, chromaW, chromaH int, qf QuantFactors) {
	for mbY := 0; mbY < mbH; mbY++ {
		for mbX := 0; mbX < mbW; mbX++ {
			mbIdx := mbY*mbW + mbX
			srcY := extractLumaBlock(frame, mbX, mbY, e.width, e.height)
			srcU, srcV := extractChromaBlocks(frame, mbX, mbY, chromaW, chromaH)

			// From the reconstruction, not the source: see processAllMacroblocks.
			ctx := buildReconContext(recon, mbX, mbY, e.width, e.height, chromaW)
			mbs[mbIdx] = processMacroblock(srcY, srcU, srcV, ctx, qf)
			reconstructIntraMBWithContext(recon, &mbs[mbIdx], ctx, mbX, mbY, e.width, chromaW, qf)
		}
	}
}

// processInterFrameMBs analyses and reconstructs macroblocks for an inter frame.
func (e *Encoder) processInterFrameMBs(frame *Frame, mbs []macroblock, recon *refFrameBuffer, mbW, mbH, chromaW, chromaH int, qf QuantFactors) {
	refBuf := e.refFrames.getRef(refFrameLast)
	dirty := e.dirtyMap
	if len(dirty) != len(mbs) {
		dirty = nil
	}
	for mbY := 0; mbY < mbH; mbY++ {
		for mbX := 0; mbX < mbW; mbX++ {
			mbIdx := mbY*mbW + mbX
			if dirty != nil && !dirty[mbIdx] {
				mbs[mbIdx] = unchangedMacroblock()
				reconstructInterMB(recon, &mbs[mbIdx], mbX, mbY, e.width, e.height, chromaW, qf, e.refFrames)
				continue
			}
			srcY := extractLumaBlock(frame, mbX, mbY, e.width, e.height)
			srcU, srcV := extractChromaBlocks(frame, mbX, mbY, chromaW, chromaH)
			ctx := buildReconContext(recon, mbX, mbY, e.width, e.height, chromaW)
			mbs[mbIdx] = processInterMacroblock(srcY, srcU, srcV, refBuf, mbX, mbY, mbW, mbs, qf, ctx, e.screenContent)

			if mbs[mbIdx].isInter {
				reconstructInterMB(recon, &mbs[mbIdx], mbX, mbY, e.width, e.height, chromaW, qf, e.refFrames)
			} else {
				reconstructIntraMBWithContext(recon, &mbs[mbIdx], ctx, mbX, mbY, e.width, chromaW, qf)
			}
		}
	}
}

// unchangedMacroblock is a skipped ZEROMV reference to the last frame: the
// cheapest macroblock VP8 can express, and an exact copy of what the decoder
// already holds.
func unchangedMacroblock() macroblock {
	return macroblock{
		skip:      true,
		isInter:   true,
		refFrame:  refFrameLast,
		mv:        zeroMV,
		interMode: mvModeZeroMV,
	}
}

// encodeKeyFrame builds and returns the key frame bitstream.
func (e *Encoder) encodeKeyFrame(mbs []macroblock, recon refFrameBuffer) ([]byte, error) {
	result, err := e.buildKeyFrameBitstream(mbs)
	if err != nil {
		return nil, err
	}

	e.reconstructAndStore(recon, true, true)
	e.frameCount = 1
	e.forceNextKeyFrame = false

	return result, nil
}

// buildKeyFrameBitstream constructs the key frame bitstream with optional probability updates.
func (e *Encoder) buildKeyFrameBitstream(mbs []macroblock) ([]byte, error) {
	// Per VP8 spec, all probabilities reset to defaults on key frames.
	// Reset coeffProbs to defaults before encoding the key frame.
	e.coeffProbs = DefaultCoeffProbs

	if !e.useProbUpdates {
		return BuildKeyFrame(e.width, e.height, e.qi,
			e.y1DCDelta, e.y2DCDelta, e.y2ACDelta, e.uvDCDelta, e.uvACDelta,
			e.partitionCount, e.loopFilter, mbs)
	}

	probCfg := e.buildProbConfig()
	result, err := buildKeyFrameWithProbs(e.width, e.height, e.qi,
		e.y1DCDelta, e.y2DCDelta, e.y2ACDelta, e.uvDCDelta, e.uvACDelta,
		e.partitionCount, e.loopFilter, mbs, probCfg)
	e.coeffHistogram.Reset()
	return result, err
}

// encodeInterFrame builds and returns the inter frame bitstream.
func (e *Encoder) encodeInterFrame(mbs []macroblock, recon refFrameBuffer) ([]byte, error) {
	refreshGolden := e.shouldUpdateGolden()

	result, err := e.buildInterFrameBitstream(mbs, refreshGolden)
	if err != nil {
		return nil, err
	}

	e.reconstructAndStore(recon, false, refreshGolden)
	e.frameCount++

	return result, nil
}

// buildInterFrameBitstream constructs the inter frame bitstream with optional probability updates.
func (e *Encoder) buildInterFrameBitstream(mbs []macroblock, refreshGolden bool) ([]byte, error) {
	if !e.useProbUpdates {
		return BuildInterFrame(e.width, e.height, e.qi,
			e.y1DCDelta, e.y2DCDelta, e.y2ACDelta, e.uvDCDelta, e.uvACDelta,
			e.partitionCount, e.loopFilter, refreshGolden, mbs)
	}

	probCfg := e.buildProbConfig()
	return buildInterFrameWithProbs(e.width, e.height, e.qi,
		e.y1DCDelta, e.y2DCDelta, e.y2ACDelta, e.uvDCDelta, e.uvACDelta,
		e.partitionCount, e.loopFilter, refreshGolden, mbs, probCfg)
}

// buildProbConfig creates the probability configuration for coefficient encoding.
func (e *Encoder) buildProbConfig() *ProbConfig {
	newProbs := e.coeffHistogram.ComputeUpdatedProbs(&e.coeffProbs)
	if e.coeffHistogram.ShouldUpdate(&e.coeffProbs, &newProbs) {
		return &ProbConfig{
			CurrentProbs: &e.coeffProbs,
			NewProbs:     &newProbs,
			Histogram:    e.coeffHistogram,
		}
	}
	return &ProbConfig{
		CurrentProbs: &e.coeffProbs,
		Histogram:    e.coeffHistogram,
	}
}

// shouldEncodeKeyFrame determines whether the next frame should be a key frame.
func (e *Encoder) shouldEncodeKeyFrame() bool {
	// Forced key frame
	if e.forceNextKeyFrame {
		return true
	}
	// I-frame only mode (backward compatible default)
	if e.keyFrameInterval == 0 {
		return true
	}
	// First frame ever (no frames encoded yet)
	if e.frameCount == 0 {
		return true
	}
	// Key frame interval reached
	if e.frameCount >= e.keyFrameInterval {
		return true
	}
	// No valid reference frame
	if !e.refFrames.hasReference(refFrameLast) {
		return true
	}
	return false
}

// reconstructAndStore reconstructs the encoded frame and stores it as a reference.
// For key frames, golden is also updated. For inter frames, golden is updated
// based on the refreshGolden parameter (which must match what was signaled in the bitstream).
func (e *Encoder) reconstructAndStore(recon refFrameBuffer, isKeyFrame, refreshGolden bool) {
	// Apply loop filter to reconstructed reference frame if enabled.
	// The loop filter level is encoded in the frame header, ensuring
	// encoder and decoder apply the same filtering to reference frames.
	applyLoopFilter(&recon, e.loopFilter)

	// Store as last reference frame
	e.refFrames.updateLast(recon.Y, recon.Cb, recon.Cr)

	// Update golden frame if needed
	if isKeyFrame || refreshGolden {
		// Key frames always update golden; inter frames update when signaled
		e.refFrames.copyLastToGolden()
		e.goldenFrameCount = 0
		e.forceNextGolden = false
	} else {
		e.goldenFrameCount++
	}
}

// shouldUpdateGolden determines whether the golden frame should be updated.
func (e *Encoder) shouldUpdateGolden() bool {
	if e.forceNextGolden {
		return true
	}
	if e.goldenFrameInterval > 0 && e.goldenFrameCount >= e.goldenFrameInterval {
		return true
	}
	return false
}

// extractLumaBlock extracts a 16x16 luma block from the frame.
func extractLumaBlock(frame *Frame, mbX, mbY, width, height int) []byte {
	var srcY [256]byte
	for row := 0; row < 16; row++ {
		srcRow := mbY*16 + row
		if srcRow >= height {
			srcRow = height - 1
		}
		for col := 0; col < 16; col++ {
			srcCol := mbX*16 + col
			if srcCol >= width {
				srcCol = width - 1
			}
			srcY[row*16+col] = frame.Y[srcRow*width+srcCol]
		}
	}
	return srcY[:]
}

// extractChromaBlocks extracts 8x8 U and V chroma blocks from the frame.
func extractChromaBlocks(frame *Frame, mbX, mbY, chromaW, chromaH int) ([]byte, []byte) {
	var srcU, srcV [64]byte
	for row := 0; row < 8; row++ {
		srcRow := mbY*8 + row
		if srcRow >= chromaH {
			srcRow = chromaH - 1
		}
		for col := 0; col < 8; col++ {
			srcCol := mbX*8 + col
			if srcCol >= chromaW {
				srcCol = chromaW - 1
			}
			srcU[row*8+col] = frame.Cb[srcRow*chromaW+srcCol]
			srcV[row*8+col] = frame.Cr[srcRow*chromaW+srcCol]
		}
	}
	return srcU[:], srcV[:]
}

// buildMBContext extracts neighbor pixels for prediction.
// Uses fixed-size backing arrays in mbContext to avoid per-MB heap allocations.
func (e *Encoder) buildMBContext(frame *Frame, mbX, mbY, mbW, mbH int) *mbContext {
	ctx := &mbContext{}
	chromaW := e.width / 2
	chromaH := e.height / 2

	buildLumaContext(ctx, frame.Y, mbX, mbY, e.width, e.height)
	buildChromaContext(ctx, frame.Cb, frame.Cr, mbX, mbY, chromaW, chromaH)

	return ctx
}

// buildLumaContext fills the luma neighbor context from the source frame.
func buildLumaContext(ctx *mbContext, y []byte, mbX, mbY, width, height int) {
	if mbY > 0 {
		fillAboveRow(ctx.lumaAboveBuf[:], y, mbX*16, (mbY*16-1)*width, width, 20)
		ctx.lumaAbove = ctx.lumaAboveBuf[:]
	}
	if mbX > 0 {
		fillLeftCol(ctx.lumaLeftBuf[:], y, mbX*16-1, mbY*16, width, height, 16)
		ctx.lumaLeft = ctx.lumaLeftBuf[:]
	}
	ctx.lumaTopLeft = computeTopLeft(y, mbX*16, mbY*16, width, mbY > 0, mbX > 0)
}

// buildChromaContext fills the chroma neighbor context from the source frame.
func buildChromaContext(ctx *mbContext, cb, cr []byte, mbX, mbY, chromaW, chromaH int) {
	if mbY > 0 {
		aboveRow := (mbY*8 - 1) * chromaW
		fillAboveRow(ctx.chromaAboveUBuf[:], cb, mbX*8, aboveRow, chromaW, 8)
		fillAboveRow(ctx.chromaAboveVBuf[:], cr, mbX*8, aboveRow, chromaW, 8)
		ctx.chromaAboveU = ctx.chromaAboveUBuf[:]
		ctx.chromaAboveV = ctx.chromaAboveVBuf[:]
	}
	if mbX > 0 {
		fillLeftCol(ctx.chromaLeftUBuf[:], cb, mbX*8-1, mbY*8, chromaW, chromaH, 8)
		fillLeftCol(ctx.chromaLeftVBuf[:], cr, mbX*8-1, mbY*8, chromaW, chromaH, 8)
		ctx.chromaLeftU = ctx.chromaLeftUBuf[:]
		ctx.chromaLeftV = ctx.chromaLeftVBuf[:]
	}
	ctx.chromaTopLeftU = computeTopLeft(cb, mbX*8, mbY*8, chromaW, mbY > 0, mbX > 0)
	ctx.chromaTopLeftV = computeTopLeft(cr, mbX*8, mbY*8, chromaW, mbY > 0, mbX > 0)
}

// fillAboveRow fills the above row buffer from the source plane.
func fillAboveRow(buf, src []byte, startCol, rowOffset, planeW, count int) {
	last := byte(127)
	for i := 0; i < count; i++ {
		col := startCol + i
		if col < planeW {
			buf[i] = src[rowOffset+col]
			last = buf[i]
			continue
		}
		// Past the right edge of the frame. This is the above-right of the last
		// macroblock in a row, and the format replicates the last pixel of the
		// row above rather than reading beyond it. Leaving the previous
		// macroblock's values here instead is a silent mismatch.
		buf[i] = last
	}
}

// fillLeftCol fills the left column buffer from the source plane.
func fillLeftCol(buf, src []byte, col, startRow, planeW, planeH, count int) {
	for i := 0; i < count; i++ {
		row := startRow + i
		if row < planeH {
			buf[i] = src[row*planeW+col]
		}
	}
}

// computeTopLeft returns the pixel diagonally above and to the left of a
// macroblock, which TM_PRED and several B_PRED sub-modes read directly.
//
// Outside the frame the format does not use a neutral grey. The row above the
// frame reads 127 and the column to its left reads 129, and the corner belongs
// to whichever of the two is outside: above the first row it is 127, and to the
// left of the first column of any later row it is 129. Answering 128 for both
// -- as this did -- is wrong by one or two levels, which is invisible in a key
// frame whose sub-modes happen not to read the corner and shows up as a small
// persistent drift the moment one does.
func computeTopLeft(src []byte, x, y, planeW int, hasAbove, hasLeft bool) byte {
	switch {
	case hasAbove && hasLeft:
		return src[(y-1)*planeW+(x-1)]
	case !hasAbove:
		return 127
	default:
		return 129
	}
}

// Width returns the configured frame width in pixels.
func (e *Encoder) Width() int { return e.width }

// Height returns the configured frame height in pixels.
func (e *Encoder) Height() int { return e.height }

// FPS returns the configured frame rate.
func (e *Encoder) FPS() int { return e.fps }

// SetScreenContentProfile restricts inter macroblocks to ZEROMV, skip and intra,
// which suits a desktop being shared rather than camera video.
//
// Between two frames of a screen, most macroblocks are byte-identical: a cursor
// moves, one window repaints, everything else is unchanged. A motion search
// spends its time confirming that the best vector is zero, and VP8 codes an
// unchanged macroblock in roughly one bit.
//
// It is also the safe path. MV_NEW codes only a delta against the predictor the
// *decoder* derives, and this encoder's predictor is documented as possibly
// diverging from RFC 6386 section 18.2; measured through libvpx, inter frames
// on that path reconstruct at 8-12 dB rather than the ~39 dB key frames manage.
// Staying on ZEROMV keeps the encoder out of that path entirely.
//
// Default is false, which preserves the upstream behaviour.
// SetDirtyMacroblocks tells the encoder which macroblocks changed since the
// previous frame, in raster order, for the next Encode call only.
//
// A macroblock the caller reports as clean is coded as a skipped ZEROMV
// reference to the last frame without any analysis: no motion search, no mode
// decision, no transform. That is not an approximation. The source pixels are
// unchanged, so copying the previous reconstruction is what the encoder would
// have chosen anyway; the difference is that it costs nothing to decide. Any
// quantisation error already present is carried forward rather than compounded,
// and the periodic key frame discards it.
//
// Passing a map of the wrong length, or none at all, means every macroblock is
// analysed. That is the safe direction: a caller that gets its bookkeeping
// wrong loses speed, not correctness. A caller that wrongly reports a *changed*
// macroblock as clean would freeze it on screen until the next key frame, so
// callers should mark a macroblock dirty whenever they are unsure.
func (e *Encoder) SetDirtyMacroblocks(dirty []bool) {
	e.dirtyMap = dirty
}

func (e *Encoder) SetScreenContentProfile(enabled bool) {
	e.screenContent = enabled
}
