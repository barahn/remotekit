package vp8

// This file implements VP8 reference frame buffer management.
// VP8 maintains three reference frame buffers:
//   - Last: the most recently encoded frame
//   - Golden: a selected reference frame for longer-term prediction
//   - AltRef: an alternate reference frame
//
// Reference: RFC 6386 §9.8 – Reference Frame Buffer Management

// refFrameType identifies which reference frame buffer to use for prediction.
type refFrameType uint8

const (
	// refFrameCurrent is a sentinel used during encoding (intra prediction).
	refFrameCurrent refFrameType = iota
	// refFrameLast is the most recently encoded frame.
	refFrameLast
	// refFrameGolden is the golden reference frame.
	refFrameGolden
	// refFrameAltRef is the alternate reference frame.
	refFrameAltRef
)

// refFrameBuffer holds a single reconstructed reference frame in YUV420 format.
type refFrameBuffer struct {
	// Y is the reconstructed luma plane.
	Y []byte
	// Cb is the reconstructed Cb chroma plane.
	Cb []byte
	// Cr is the reconstructed Cr chroma plane.
	Cr []byte
	// Width and Height are the frame dimensions.
	Width, Height int
	// valid indicates whether this buffer has been initialized.
	valid bool
}

// refFrameManager manages the three VP8 reference frame buffers.
// After each frame is encoded and reconstructed, the buffers are updated
// according to VP8 refresh flags.
type refFrameManager struct {
	last   refFrameBuffer
	golden refFrameBuffer
	altRef refFrameBuffer
	width  int
	height int
}

// newRefFrameManager creates a new reference frame manager for the given
// frame dimensions.
func newRefFrameManager(width, height int) *refFrameManager {
	return &refFrameManager{
		width:  width,
		height: height,
	}
}

// allocBuffer allocates a reference frame buffer for the configured dimensions.
func (m *refFrameManager) allocBuffer() refFrameBuffer {
	ySize := m.width * m.height
	uvSize := (m.width / 2) * (m.height / 2)
	return refFrameBuffer{
		Y:      make([]byte, ySize),
		Cb:     make([]byte, uvSize),
		Cr:     make([]byte, uvSize),
		Width:  m.width,
		Height: m.height,
		valid:  false,
	}
}

// getRef returns the reference frame buffer for the given type.
// Returns nil if the requested buffer is not valid.
func (m *refFrameManager) getRef(ref refFrameType) *refFrameBuffer {
	switch ref {
	case refFrameLast:
		if m.last.valid {
			return &m.last
		}
	case refFrameGolden:
		if m.golden.valid {
			return &m.golden
		}
	case refFrameAltRef:
		if m.altRef.valid {
			return &m.altRef
		}
	}
	return nil
}

// hasReference returns true if the specified reference frame is available.
func (m *refFrameManager) hasReference(ref refFrameType) bool {
	return m.getRef(ref) != nil
}

// updateLast updates the last reference frame buffer with the given
// reconstructed frame data. This is called after every key frame and
// after inter frames with refresh_last set.
func (m *refFrameManager) updateLast(y, cb, cr []byte) {
	if !m.last.valid {
		m.last = m.allocBuffer()
		m.last.valid = true
	}
	copy(m.last.Y, y)
	copy(m.last.Cb, cb)
	copy(m.last.Cr, cr)
}

// updateGolden updates the golden reference frame buffer.
func (m *refFrameManager) updateGolden(y, cb, cr []byte) {
	if !m.golden.valid {
		m.golden = m.allocBuffer()
		m.golden.valid = true
	}
	copy(m.golden.Y, y)
	copy(m.golden.Cb, cb)
	copy(m.golden.Cr, cr)
}

// updateAltRef updates the alternate reference frame buffer.
func (m *refFrameManager) updateAltRef(y, cb, cr []byte) {
	if !m.altRef.valid {
		m.altRef = m.allocBuffer()
		m.altRef.valid = true
	}
	copy(m.altRef.Y, y)
	copy(m.altRef.Cb, cb)
	copy(m.altRef.Cr, cr)
}

// copyLastToGolden copies the last frame buffer to the golden buffer.
// This is used when refresh_golden_frame is set with copy_buffer_to_gf=1.
func (m *refFrameManager) copyLastToGolden() {
	if !m.last.valid {
		return
	}
	if !m.golden.valid {
		m.golden = m.allocBuffer()
		m.golden.valid = true
	}
	copy(m.golden.Y, m.last.Y)
	copy(m.golden.Cb, m.last.Cb)
	copy(m.golden.Cr, m.last.Cr)
}

// copyLastToAltRef copies the last frame buffer to the alternate reference.
func (m *refFrameManager) copyLastToAltRef() {
	if !m.last.valid {
		return
	}
	if !m.altRef.valid {
		m.altRef = m.allocBuffer()
		m.altRef.valid = true
	}
	copy(m.altRef.Y, m.last.Y)
	copy(m.altRef.Cb, m.last.Cb)
	copy(m.altRef.Cr, m.last.Cr)
}

// reset invalidates all reference frame buffers.
// This is called when dimensions change or on encoder reset.
func (m *refFrameManager) reset() {
	m.last.valid = false
	m.golden.valid = false
	m.altRef.valid = false
}

// reconstructFrame reconstructs a full frame from encoded macroblocks and stores
// it in the provided output buffers. This is needed to build reference frames for
// inter-frame prediction.
//
// For key frames: reconstructs from intra predictions.
// For inter frames: reconstructs from motion-compensated predictions.
func reconstructFrame(recon *refFrameBuffer, mbs []macroblock, qf QuantFactors,
	ref *refFrameManager, frame *Frame,
) {
	width := recon.Width
	height := recon.Height
	mbW := (width + 15) / 16
	mbH := (height + 15) / 16
	chromaW := width / 2

	for mbY := 0; mbY < mbH; mbY++ {
		for mbX := 0; mbX < mbW; mbX++ {
			mbIdx := mbY*mbW + mbX
			mb := &mbs[mbIdx]

			if mb.isInter {
				// Inter-frame reconstruction: motion-compensated prediction + residual
				reconstructInterMB(recon, mb, mbX, mbY, width, height, chromaW, qf, ref)
			} else {
				// Intra-frame reconstruction: intra prediction + residual
				reconstructIntraMB(recon, mb, mbX, mbY, width, height, chromaW, qf)
			}
		}
	}
}

// reconstructIntraMB reconstructs a single intra-predicted macroblock, deriving
// the neighbour context from the reconstruction itself.
func reconstructIntraMB(recon *refFrameBuffer, mb *macroblock, mbX, mbY, width, height, chromaW int, qf QuantFactors) {
	ctx := buildReconContext(recon, mbX, mbY, width, height, chromaW)
	reconstructIntraMBWithContext(recon, mb, ctx, mbX, mbY, width, chromaW, qf)
}

// reconstructIntraMBWithContext reconstructs an intra macroblock against a
// context the caller already has.
//
// The encoder analyses and reconstructs each macroblock in one step so that both
// use the SAME context — the one derived from the reconstruction. Building it
// twice would risk the two drifting apart, which is the defect this structure
// exists to remove.
func reconstructIntraMBWithContext(recon *refFrameBuffer, mb *macroblock, ctx *mbContext, mbX, mbY, width, chromaW int, qf QuantFactors) {
	// Reconstruct luma
	if mb.lumaMode == B_PRED {
		reconstructLumaBPred(recon, mb, ctx, mbX, mbY, width, qf)
	} else {
		reconstructLuma16x16(recon, mb, ctx, mbX, mbY, width, qf)
	}

	// Reconstruct chroma
	reconstructChroma(recon, mb, ctx, mbX, mbY, width, chromaW, qf)
}

// buildReconContext builds neighbor context from the reconstructed frame buffer.
// Uses fixed-size backing arrays in mbContext to avoid per-MB heap allocations.
func buildReconContext(recon *refFrameBuffer, mbX, mbY, width, height, chromaW int) *mbContext {
	ctx := &mbContext{}
	chromaH := height / 2

	buildReconLumaContext(ctx, recon.Y, mbX, mbY, width, height)
	buildReconChromaContext(ctx, recon.Cb, recon.Cr, mbX, mbY, chromaW, chromaH)

	return ctx
}

// buildReconLumaContext fills the luma neighbor context from reconstructed frame.
func buildReconLumaContext(ctx *mbContext, y []byte, mbX, mbY, width, height int) {
	if mbY > 0 {
		aboveRow := (mbY*16 - 1) * width
		fillAboveRowRecon(ctx.lumaAboveBuf[:], y, mbX*16, aboveRow, width, 20)
		ctx.lumaAbove = ctx.lumaAboveBuf[:]
	}
	if mbX > 0 {
		fillLeftColRecon(ctx.lumaLeftBuf[:], y, mbX*16-1, mbY*16, width, height, 16)
		ctx.lumaLeft = ctx.lumaLeftBuf[:]
	}
	ctx.lumaTopLeft = computeReconTopLeft(y, mbX*16, mbY*16, width, mbY > 0, mbX > 0)
}

// buildReconChromaContext fills the chroma neighbor context from reconstructed frame.
func buildReconChromaContext(ctx *mbContext, cb, cr []byte, mbX, mbY, chromaW, chromaH int) {
	if mbY > 0 {
		aboveRow := (mbY*8 - 1) * chromaW
		fillAboveRowRecon(ctx.chromaAboveUBuf[:], cb, mbX*8, aboveRow, chromaW, 8)
		fillAboveRowRecon(ctx.chromaAboveVBuf[:], cr, mbX*8, aboveRow, chromaW, 8)
		ctx.chromaAboveU = ctx.chromaAboveUBuf[:]
		ctx.chromaAboveV = ctx.chromaAboveVBuf[:]
	}
	if mbX > 0 {
		fillLeftColRecon(ctx.chromaLeftUBuf[:], cb, mbX*8-1, mbY*8, chromaW, chromaH, 8)
		fillLeftColRecon(ctx.chromaLeftVBuf[:], cr, mbX*8-1, mbY*8, chromaW, chromaH, 8)
		ctx.chromaLeftU = ctx.chromaLeftUBuf[:]
		ctx.chromaLeftV = ctx.chromaLeftVBuf[:]
	}
	ctx.chromaTopLeftU = computeReconTopLeft(cb, mbX*8, mbY*8, chromaW, mbY > 0, mbX > 0)
	ctx.chromaTopLeftV = computeReconTopLeft(cr, mbX*8, mbY*8, chromaW, mbY > 0, mbX > 0)
}

// fillAboveRowRecon fills the above row buffer from the reconstructed plane.
func fillAboveRowRecon(buf, src []byte, startCol, rowOffset, planeW, count int) {
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

// fillLeftColRecon fills the left column buffer from the reconstructed plane.
func fillLeftColRecon(buf, src []byte, col, startRow, planeW, planeH, count int) {
	for i := 0; i < count; i++ {
		row := startRow + i
		if row < planeH {
			buf[i] = src[row*planeW+col]
		}
	}
}

// computeReconTopLeft returns the pixel diagonally above and to the left of a
// macroblock, which TM_PRED and several B_PRED sub-modes read directly.
//
// Outside the frame the format does not use a neutral grey. The row above the
// frame reads 127 and the column to its left reads 129, and the corner belongs
// to whichever of the two is outside: above the first row it is 127, and to the
// left of the first column of any later row it is 129. Answering 128 for both
// -- as this did -- is wrong by one or two levels, which is invisible in a key
// frame whose sub-modes happen not to read the corner and shows up as a small
// persistent drift the moment one does.
func computeReconTopLeft(src []byte, x, y, planeW int, hasAbove, hasLeft bool) byte {
	switch {
	case hasAbove && hasLeft:
		return src[(y-1)*planeW+(x-1)]
	case !hasAbove:
		return 127
	default:
		return 129
	}
}

// reconstructLuma16x16 reconstructs luma using 16x16 prediction mode.
func reconstructLuma16x16(recon *refFrameBuffer, mb *macroblock, ctx *mbContext, mbX, mbY, width int, qf QuantFactors) {
	// Generate 16x16 prediction
	var predY [256]byte
	Predict16x16(predY[:], ctx.lumaAbove, ctx.lumaLeft, ctx.lumaTopLeft, mb.lumaMode)

	// Dequantize Y2 (WHT DC block)
	dequantWHT := DequantizeBlock(FromZigzag(mb.y2Coeffs), qf.Y2DC, qf.Y2AC)
	dcValues := InverseWHT4x4(dequantWHT)

	// Reconstruct all 16 4x4 luma blocks using shared helper
	for by := 0; by < 4; by++ {
		for bx := 0; bx < 4; bx++ {
			reconstructLuma4x4WithDC(recon, mb, predY[:], dcValues, by, bx, mbX, mbY, width, qf)
		}
	}
}

// reconstructLuma4x4WithDC reconstructs a single 4x4 luma block with DC from WHT.
// Shared helper for both intra 16x16 and inter reconstruction.
func reconstructLuma4x4WithDC(recon *refFrameBuffer, mb *macroblock, predY []byte, dcValues [16]int16, by, bx, mbX, mbY, width int, qf QuantFactors) {
	blockIdx := by*4 + bx

	zigzagCoeffs := mb.yCoeffs[blockIdx]
	rasterCoeffs := FromZigzag(zigzagCoeffs)
	rasterCoeffs[0] = dcValues[blockIdx]
	dequantized := DequantizeBlock(rasterCoeffs, qf.Y1DC, qf.Y1AC)
	// DC was dequantized through WHT, use it directly
	dequantized[0] = dcValues[blockIdx]

	invDCT := InverseDCT4x4(dequantized)

	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			py := mbY*16 + by*4 + row
			px := mbX*16 + bx*4 + col
			if py < recon.Height && px < width {
				predIdx := (by*4+row)*16 + bx*4 + col
				val := int(predY[predIdx]) + int(invDCT[row*4+col])
				recon.Y[py*width+px] = clamp8(val)
			}
		}
	}
}

// reconstructLumaBPred reconstructs luma using B_PRED mode.
func reconstructLumaBPred(recon *refFrameBuffer, mb *macroblock, ctx *mbContext, mbX, mbY, width int, qf QuantFactors) {
	// Local reconstruction buffer for inter-block dependencies
	var localRecon [256]byte

	for by := 0; by < 4; by++ {
		for bx := 0; bx < 4; bx++ {
			blockIdx := by*4 + bx

			above, left := build4x4Context(by, bx, ctx, localRecon[:])

			var pred4x4 [16]byte
			Predict4x4(pred4x4[:], above, left, mb.bModes[blockIdx])

			// Dequantize
			zigzagCoeffs := mb.yCoeffs[blockIdx]
			rasterCoeffs := FromZigzag(zigzagCoeffs)
			dequantized := DequantizeBlock(rasterCoeffs, qf.Y1DC, qf.Y1AC)
			invDCT := InverseDCT4x4(dequantized)

			for row := 0; row < 4; row++ {
				for col := 0; col < 4; col++ {
					val := int(pred4x4[row*4+col]) + int(invDCT[row*4+col])
					clamped := clamp8(val)
					localRecon[(by*4+row)*16+bx*4+col] = clamped

					py := mbY*16 + by*4 + row
					px := mbX*16 + bx*4 + col
					if py < recon.Height && px < width {
						recon.Y[py*width+px] = clamped
					}
				}
			}
		}
	}
}

// reconstructChroma reconstructs the chroma planes for a macroblock.
func reconstructChroma(recon *refFrameBuffer, mb *macroblock, ctx *mbContext, mbX, mbY, width, chromaW int, qf QuantFactors) {
	chromaH := recon.Height / 2

	// U plane
	var predU [64]byte
	Predict8x8Chroma(predU[:], ctx.chromaAboveU, ctx.chromaLeftU, ctx.chromaTopLeftU, mb.chromaMode)
	reconstructChromaPlane(recon.Cb, mb.uCoeffs[:], predU[:], mbX, mbY, chromaW, chromaH, qf)

	// V plane
	var predV [64]byte
	Predict8x8Chroma(predV[:], ctx.chromaAboveV, ctx.chromaLeftV, ctx.chromaTopLeftV, mb.chromaMode)
	reconstructChromaPlane(recon.Cr, mb.vCoeffs[:], predV[:], mbX, mbY, chromaW, chromaH, qf)
}

// reconstructChromaPlane reconstructs a single chroma plane (U or V).
func reconstructChromaPlane(dst []byte, coeffs [][16]int16, pred []byte, mbX, mbY, chromaW, chromaH int, qf QuantFactors) {
	for by := 0; by < 2; by++ {
		for bx := 0; bx < 2; bx++ {
			reconstructChroma4x4(dst, coeffs, pred, by, bx, mbX, mbY, chromaW, chromaH, qf)
		}
	}
}

// reconstructChroma4x4 reconstructs a single 4x4 chroma block.
func reconstructChroma4x4(dst []byte, coeffs [][16]int16, pred []byte, by, bx, mbX, mbY, chromaW, chromaH int, qf QuantFactors) {
	blockIdx := by*2 + bx
	rasterCoeffs := FromZigzag(coeffs[blockIdx])
	dequantized := DequantizeBlock(rasterCoeffs, qf.UVDC, qf.UVAC)
	invDCT := InverseDCT4x4(dequantized)

	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			py := mbY*8 + by*4 + row
			px := mbX*8 + bx*4 + col
			if py < chromaH && px < chromaW {
				predIdx := (by*4+row)*8 + bx*4 + col
				val := int(pred[predIdx]) + int(invDCT[row*4+col])
				dst[py*chromaW+px] = clamp8(val)
			}
		}
	}
}

// reconstructInterMB reconstructs a single inter-predicted macroblock using
// motion compensation from the reference frame.
func reconstructInterMB(recon *refFrameBuffer, mb *macroblock, mbX, mbY, width, height, chromaW int, qf QuantFactors, ref *refFrameManager) {
	refBuf := ref.getRef(mb.refFrame)
	if refBuf == nil {
		reconstructIntraMB(recon, mb, mbX, mbY, width, height, chromaW, qf)
		return
	}

	// A skipped macroblock carries no coefficients, so its reconstruction is
	// exactly its prediction. With a zero motion vector the prediction is the
	// co-located block of the reference, which makes the whole macroblock a
	// straight copy -- no dequantisation, no inverse transforms, no per-4x4
	// loop over blocks that are all zero.
	//
	// This is the overwhelming majority of macroblocks in a screen stream, and
	// it was previously costing the full transform path: inverse-DCT'ing blocks
	// of zeros and adding them to a prediction they could not change. At 1080p
	// that was two thirds of the time spent on an inter frame.
	if mb.skip && mb.mv == zeroMV && !hasNonZeroCoeffs(mb.y2Coeffs[:]) {
		copyMacroblockFromRef(recon, refBuf, mbX, mbY, width, height, chromaW)
		return
	}

	// Reconstruct luma with motion compensation
	reconstructInterLuma(recon, mb, refBuf, mbX, mbY, width, height, qf)

	// Reconstruct chroma with halved MV
	reconstructInterChroma(recon, mb, refBuf, mbX, mbY, width, height, chromaW, qf)
}

// copyMacroblockFromRef copies one macroblock's three planes straight across
// from the reference frame, row by row.
//
// Both buffers have the same geometry, so this is a run of memmoves rather than
// the clamped per-pixel copy motion compensation needs for vectors that point
// outside the frame -- a zero vector never does.
func copyMacroblockFromRef(recon, refBuf *refFrameBuffer, mbX, mbY, width, height, chromaW int) {
	x0, y0 := mbX*16, mbY*16
	for row := 0; row < 16; row++ {
		y := y0 + row
		if y >= height {
			break
		}
		w := 16
		if x0+w > width {
			w = width - x0
		}
		if w <= 0 {
			break
		}
		off := y*width + x0
		copy(recon.Y[off:off+w], refBuf.Y[off:off+w])
	}

	chromaH := height / 2
	cx0, cy0 := mbX*8, mbY*8
	for row := 0; row < 8; row++ {
		y := cy0 + row
		if y >= chromaH {
			break
		}
		w := 8
		if cx0+w > chromaW {
			w = chromaW - cx0
		}
		if w <= 0 {
			break
		}
		off := y*chromaW + cx0
		copy(recon.Cb[off:off+w], refBuf.Cb[off:off+w])
		copy(recon.Cr[off:off+w], refBuf.Cr[off:off+w])
	}
}

// reconstructInterLuma reconstructs luma blocks using motion-compensated prediction.
func reconstructInterLuma(recon *refFrameBuffer, mb *macroblock, refBuf *refFrameBuffer, mbX, mbY, width, height int, qf QuantFactors) {
	var predY [256]byte
	motionCompensate16x16(predY[:], refBuf.Y, width, height, mbX*16, mbY*16, mb.mv)

	dequantWHT := DequantizeBlock(FromZigzag(mb.y2Coeffs), qf.Y2DC, qf.Y2AC)
	dcValues := InverseWHT4x4(dequantWHT)

	// Use shared helper for 4x4 block reconstruction
	for by := 0; by < 4; by++ {
		for bx := 0; bx < 4; bx++ {
			reconstructLuma4x4WithDC(recon, mb, predY[:], dcValues, by, bx, mbX, mbY, width, qf)
		}
	}
}

// reconstructInterChroma reconstructs chroma planes using motion-compensated prediction.
func reconstructInterChroma(recon *refFrameBuffer, mb *macroblock, refBuf *refFrameBuffer, mbX, mbY, width, height, chromaW int, qf QuantFactors) {
	chromaH := height / 2
	chromaMV := motionVector{
		dx: mb.mv.dx / 2,
		dy: mb.mv.dy / 2,
	}

	var predU, predV [64]byte
	motionCompensate8x8(predU[:], refBuf.Cb, chromaW, chromaH, mbX*8, mbY*8, chromaMV)
	motionCompensate8x8(predV[:], refBuf.Cr, chromaW, chromaH, mbX*8, mbY*8, chromaMV)

	for by := 0; by < 2; by++ {
		for bx := 0; bx < 2; bx++ {
			reconstructInterChroma4x4(recon.Cb, mb.uCoeffs, predU[:], by, bx, mbX, mbY, chromaW, chromaH, qf)
			reconstructInterChroma4x4(recon.Cr, mb.vCoeffs, predV[:], by, bx, mbX, mbY, chromaW, chromaH, qf)
		}
	}
}

// reconstructInterChroma4x4 reconstructs a single 4x4 chroma block.
func reconstructInterChroma4x4(dst []byte, coeffs [4][16]int16, pred []byte, by, bx, mbX, mbY, chromaW, chromaH int, qf QuantFactors) {
	blockIdx := by*2 + bx
	raster := FromZigzag(coeffs[blockIdx])
	dequant := DequantizeBlock(raster, qf.UVDC, qf.UVAC)
	inv := InverseDCT4x4(dequant)

	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			py := mbY*8 + by*4 + row
			px := mbX*8 + bx*4 + col
			if py < chromaH && px < chromaW {
				predIdx := (by*4+row)*8 + bx*4 + col
				val := int(pred[predIdx]) + int(inv[row*4+col])
				dst[py*chromaW+px] = clamp8(val)
			}
		}
	}
}
