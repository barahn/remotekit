// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

// Block types the coefficient probabilities are indexed by (RFC 6386 §13.3).
const (
	blockYAfterY2 = 0 // luma, its DC carried by the Y2 block
	blockY2       = 1
	blockChroma   = 2
	blockYWithDC  = 3 // luma in a B_PRED or SPLITMV macroblock, which has no Y2
)

// Six-tap sub-pixel filters, by eighth-pixel position (RFC 6386 §18.3).
var sixtapFilters = [8][6]int{
	{0, 0, 128, 0, 0, 0},
	{0, -6, 123, 12, -1, 0},
	{2, -11, 108, 36, -8, 1},
	{0, -9, 93, 50, -6, 0},
	{3, -16, 77, 77, -16, 3},
	{0, -6, 50, 93, -9, 0},
	{1, -8, 36, 108, -11, 2},
	{0, -1, 12, 123, -6, 0},
}

// Bilinear filters, for streams of version 1 and 2.
var bilinearFilters = [8][2]int{
	{128, 0}, {112, 16}, {96, 32}, {80, 48}, {64, 64}, {48, 80}, {32, 96}, {16, 112},
}

// mbResidual is one macroblock's dequantized coefficients, raster order:
// blocks 0-15 luma, 16-19 U, 20-23 V, 24 Y2.
type mbResidual struct {
	coeffs [25][16]int16
	nz     [25]bool
}

// quantFor returns the dequantization factors for a segment.
func (d *Decoder) quantFor(h *frameHeader, segment uint8) QuantFactors {
	q := h.qIndex
	if d.segmentation {
		if d.segmentAbs {
			q = d.segmentQuant[segment]
		} else {
			q += d.segmentQuant[segment]
		}
	}
	q = clampQI(q)
	return GetQuantFactors(q, h.qDelta[0], h.qDelta[1], h.qDelta[2], h.qDelta[3], h.qDelta[4])
}

// decodeMacroblocks reads the residual of every macroblock and reconstructs
// the frame into cur, unfiltered: intra prediction reads unfiltered
// neighbours, so the loop filter runs over the whole frame afterwards.
func (d *Decoder) decodeMacroblocks(cur *decFrame, parts [][]byte, h *frameHeader) {
	brs := make([]*boolDecoder, len(parts))
	for i, p := range parts {
		brs[i] = newBoolDecoder(p)
	}
	var quant [4]QuantFactors
	for s := range quant {
		quant[s] = d.quantFor(h, uint8(s))
	}
	for i := range d.nzTop {
		d.nzTop[i] = 0
	}
	var res mbResidual
	for mbY := 0; mbY < d.mbH; mbY++ {
		br := brs[mbY%len(brs)]
		var nzLeft [9]uint8
		for mbX := 0; mbX < d.mbW; mbX++ {
			mb := &d.mbs[mbY*d.mbW+mbX]
			res = mbResidual{}
			hasY2 := mb.yMode != uint8(B_PRED) && mb.yMode != modeSplit
			top := d.nzTop[9*mbX : 9*mbX+9]
			if mb.skip {
				// No tokens. The contexts read as empty, except Y2's
				// for a macroblock that has none (libvpx
				// vp8_reset_mb_tokens_context).
				for i := 0; i < 8; i++ {
					top[i], nzLeft[i] = 0, 0
				}
				if hasY2 {
					top[8], nzLeft[8] = 0, 0
				}
				mb.hasCoeff = false
			} else {
				mb.hasCoeff = d.readResidual(br, &res, top, &nzLeft, hasY2, quant[mb.segment])
			}
			d.reconstruct(cur, mb, &res, mbX, mbY, h)
		}
		// The rightmost macroblock of the next row reads four pixels past
		// the right edge of this row's bottom line as its above-right. libvpx
		// replicates the last pixel there before the loop filter runs
		// (vp8_extend_mb_row).
		y := &cur.y
		row := y.off(y.w-1, 16*mbY+15)
		for i := 1; i <= 4; i++ {
			y.pix[row+i] = y.pix[row]
		}
	}
}

// readResidual reads one macroblock's tokens and dequantizes them. It reports
// whether any block had a token.
func (d *Decoder) readResidual(br *boolDecoder, res *mbResidual, top []uint8, left *[9]uint8, hasY2 bool, q QuantFactors) bool {
	found := false
	yType, first := blockYWithDC, 0
	if hasY2 {
		nz := d.readBlock(br, blockY2, 0, int(top[8]+left[8]), q.Y2DC, q.Y2AC, &res.coeffs[24])
		top[8], left[8] = nz, nz
		res.nz[24] = nz != 0
		found = found || nz != 0
		yType, first = blockYAfterY2, 1
	}
	for i := 0; i < 16; i++ {
		x, y := i&3, i>>2
		nz := d.readBlock(br, yType, first, int(top[x]+left[y]), q.Y1DC, q.Y1AC, &res.coeffs[i])
		top[x], left[y] = nz, nz
		res.nz[i] = nz != 0
		found = found || nz != 0
	}
	for i := 0; i < 8; i++ {
		x, y := 4+(i&1), 4+(i>>1&1)
		if i >= 4 {
			x, y = 6+(i&1), 6+(i>>1&1)
		}
		nz := d.readBlock(br, blockChroma, 0, int(top[x]+left[y]), q.UVDC, q.UVAC, &res.coeffs[16+i])
		top[x], left[y] = nz, nz
		res.nz[16+i] = nz != 0
		found = found || nz != 0
	}
	return found
}

// readBlock reads one block's tokens from position first (RFC 6386 §13),
// dequantizing into out in raster order. It returns 1 if the block had any
// token before its end-of-block, which is the context its neighbours read.
//
// Dequantized values are truncated to 16 bits, as libvpx stores them.
func (d *Decoder) readBlock(br *boolDecoder, typ, first, ctx int, dcq, acq int16, out *[16]int16) uint8 {
	probs := &d.coeffProbs[typ]
	n := first
	p := &probs[coeffBand[n]][ctx]
	if !br.readBool(p[0]) {
		return 0
	}
	for {
		if !br.readBool(p[1]) {
			// A zero. No end-of-block can follow one directly.
			n++
			if n == 16 {
				return 1
			}
			p = &probs[coeffBand[n]][0]
			continue
		}
		var v, next int
		if !br.readBool(p[2]) {
			v, next = 1, 1
		} else {
			next = 2
			switch {
			case !br.readBool(p[3]):
				if !br.readBool(p[4]) {
					v = 2
				} else {
					v = 3
					if br.readBool(p[5]) {
						v = 4
					}
				}
			case !br.readBool(p[6]):
				cat := 0
				if br.readBool(p[7]) {
					cat = 1
				}
				v = readExtra(br, cat)
			default:
				hi := 0
				if br.readBool(p[8]) {
					hi = 1
				}
				lo := 0
				if br.readBool(p[9+hi]) {
					lo = 1
				}
				v = readExtra(br, 2+2*hi+lo)
			}
		}
		if br.readBit() {
			v = -v
		}
		qf := acq
		if n == 0 {
			qf = dcq
		}
		out[ZigzagOrder[n]] = int16(v * int(qf))
		n++
		if n == 16 {
			return 1
		}
		p = &probs[coeffBand[n]][next]
		if !br.readBool(p[0]) {
			return 1
		}
	}
}

// readExtra reads a DCT_CAT token's value: its base plus extra bits, most
// significant first.
func readExtra(br *boolDecoder, cat int) int {
	v := 0
	for _, p := range catProbs[cat] {
		v <<= 1
		if br.readBool(p) {
			v |= 1
		}
	}
	return dctCatParams[cat].base + v
}

// reconstruct predicts one macroblock and adds its residual.
func (d *Decoder) reconstruct(cur *decFrame, mb *decMB, res *mbResidual, mbX, mbY int, h *frameHeader) {
	x0, y0 := 16*mbX, 16*mbY
	if res.nz[24] {
		// The Y2 block carries the luma blocks' DCs.
		dc := InverseWHT4x4(res.coeffs[24])
		for i := 0; i < 16; i++ {
			res.coeffs[i][0] = dc[i]
			res.nz[i] = res.nz[i] || dc[i] != 0
		}
	}

	if mb.ref == refIntra {
		if intraMode(mb.yMode) == B_PRED {
			d.predictBPred(&cur.y, mb, res, x0, y0)
		} else {
			predictIntra16(&cur.y, intraMode(mb.yMode), x0, y0, mbX > 0, mbY > 0)
			addResiduals(&cur.y, res, 0, x0, y0, 4)
		}
		predictIntraChroma(&cur.u, chromaMode(mb.uvMode), x0/2, y0/2, mbX > 0, mbY > 0)
		predictIntraChroma(&cur.v, chromaMode(mb.uvMode), x0/2, y0/2, mbX > 0, mbY > 0)
	} else {
		d.predictInter(cur, mb, x0, y0, h)
		addResiduals(&cur.y, res, 0, x0, y0, 4)
	}
	addResiduals(&cur.u, res, 16, x0/2, y0/2, 2)
	addResiduals(&cur.v, res, 20, x0/2, y0/2, 2)
}

// addResiduals adds the inverse transforms of an n x n group of blocks,
// starting at block index base, to the prediction already in plane p.
func addResiduals(p *decPlane, res *mbResidual, base, x0, y0, n int) {
	for i := 0; i < n*n; i++ {
		if res.nz[base+i] {
			addBlock(p, res.coeffs[base+i], x0+4*(i%n), y0+4*(i/n))
		}
	}
}

// addBlock adds one block's inverse DCT to the 4x4 at (x, y).
func addBlock(p *decPlane, coeffs [16]int16, x, y int) {
	r := InverseDCT4x4(coeffs)
	for j := 0; j < 4; j++ {
		row := p.off(x, y+j)
		for i := 0; i < 4; i++ {
			p.pix[row+i] = clamp8(int(p.pix[row+i]) + int(r[4*j+i]))
		}
	}
}

// predictIntra16 predicts a 16x16 luma block in place. DC averages only the
// edges inside the frame; the other modes read the 127/129 edges outside it
// (libvpx vp8_build_intra_predictors_mby_s).
func predictIntra16(p *decPlane, mode intraMode, x0, y0 int, hasLeft, hasAbove bool) {
	var above, left [16]byte
	copy(above[:], p.pix[p.off(x0, y0-1):])
	for i := range left {
		left[i] = p.pix[p.off(x0-1, y0+i)]
	}
	topLeft := p.pix[p.off(x0-1, y0-1)]
	a, l := above[:], left[:]
	if mode == DC_PRED {
		if !hasAbove {
			a = nil
		}
		if !hasLeft {
			l = nil
		}
	}
	var dst [256]byte
	Predict16x16(dst[:], a, l, topLeft, mode)
	for j := 0; j < 16; j++ {
		copy(p.pix[p.off(x0, y0+j):], dst[16*j:16*j+16])
	}
}

// predictIntraChroma predicts an 8x8 chroma block in place, with the same
// edge rules as predictIntra16.
func predictIntraChroma(p *decPlane, mode chromaMode, x0, y0 int, hasLeft, hasAbove bool) {
	var above, left [8]byte
	copy(above[:], p.pix[p.off(x0, y0-1):])
	for i := range left {
		left[i] = p.pix[p.off(x0-1, y0+i)]
	}
	topLeft := p.pix[p.off(x0-1, y0-1)]
	a, l := above[:], left[:]
	if mode == DC_PRED_CHROMA {
		if !hasAbove {
			a = nil
		}
		if !hasLeft {
			l = nil
		}
	}
	var dst [64]byte
	Predict8x8Chroma(dst[:], a, l, topLeft, mode)
	for j := 0; j < 8; j++ {
		copy(p.pix[p.off(x0, y0+j):], dst[8*j:8*j+8])
	}
}

// predictBPred predicts and reconstructs a B_PRED macroblock's sixteen 4x4
// sub-blocks in raster order, each from the ones before it.
func (d *Decoder) predictBPred(p *decPlane, mb *decMB, res *mbResidual, x0, y0 int) {
	for i := 0; i < 16; i++ {
		bx, by := i&3, i>>2
		x, y := x0+4*bx, y0+4*by
		var above [9]byte
		copy(above[:], p.pix[p.off(x-1, y-1):p.off(x-1, y-1)+9])
		if bx == 3 {
			// The rightmost column reads its above-right from the row above
			// the macroblock, whatever its row (libvpx
			// intra_prediction_down_copy).
			copy(above[5:9], p.pix[p.off(x0+16, y0-1):])
		}
		var left [4]byte
		for j := range left {
			left[j] = p.pix[p.off(x-1, y+j)]
		}
		var dst [16]byte
		Predict4x4(dst[:], above[:], left[:], mb.bModes[i])
		for j := 0; j < 4; j++ {
			copy(p.pix[p.off(x, y+j):], dst[4*j:4*j+4])
		}
		if res.nz[i] {
			addBlock(p, res.coeffs[i], x, y)
		}
	}
}

// predictInter builds an inter macroblock's prediction from its reference.
// Vectors are converted to eighth pixels, the unit the filters are indexed in.
func (d *Decoder) predictInter(cur *decFrame, mb *decMB, x0, y0 int, h *frameHeader) {
	ref := d.last
	switch mb.ref {
	case refGolden:
		ref = d.golden
	case refAltRef:
		ref = d.altref
	}
	if ref == nil {
		ref = cur // a corrupt stream naming a reference that was never set
	}
	bilinear := h.version != 0
	fullPixel := h.version == 3
	chromaMask := ^0
	if fullPixel {
		chromaMask = ^7
	}

	if mb.yMode != modeSplit {
		mv := mb.mvs[0]
		predictBlock(&cur.y, &ref.y, x0, y0, 16, 16, 2*int(mv.dx), 2*int(mv.dy), bilinear)
		cx, cy := chromaMV(2*int(mv.dx))&chromaMask, chromaMV(2*int(mv.dy))&chromaMask
		predictBlock(&cur.u, &ref.u, x0/2, y0/2, 8, 8, cx, cy, bilinear)
		predictBlock(&cur.v, &ref.v, x0/2, y0/2, 8, 8, cx, cy, bilinear)
		return
	}
	for i := 0; i < 16; i++ {
		mv := mb.mvs[i]
		predictBlock(&cur.y, &ref.y, x0+4*(i&3), y0+4*(i>>2), 4, 4, 2*int(mv.dx), 2*int(mv.dy), bilinear)
	}
	// Each chroma 4x4 moves by the average of the four luma vectors over it
	// (libvpx build_uvmvs).
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			b := 8*j + 2*i
			var sx, sy int
			for _, k := range [4]int{b, b + 1, b + 4, b + 5} {
				sx += 2 * int(mb.mvs[k].dx)
				sy += 2 * int(mb.mvs[k].dy)
			}
			cx, cy := splitChromaMV(sx)&chromaMask, splitChromaMV(sy)&chromaMask
			predictBlock(&cur.u, &ref.u, x0/2+4*i, y0/2+4*j, 4, 4, cx, cy, bilinear)
			predictBlock(&cur.v, &ref.v, x0/2+4*i, y0/2+4*j, 4, 4, cx, cy, bilinear)
		}
	}
}

// chromaMV converts a luma vector component in eighth pixels to the chroma
// plane's, rounding away from zero (libvpx vp8_build_inter16x16_predictors_mb).
func chromaMV(v int) int {
	if v < 0 {
		return (v - 1) / 2
	}
	return (v + 1) / 2
}

// splitChromaMV averages four luma components in eighth pixels into a chroma
// one, rounding away from zero.
func splitChromaMV(sum int) int {
	if sum < 0 {
		return (sum - 4) / 8
	}
	return (sum + 4) / 8
}

// predictBlock writes the w x h block at (x0, y0) of dst, predicted from ref
// displaced by (mvx, mvy) eighth pixels.
func predictBlock(dst, ref *decPlane, x0, y0, w, h, mvx, mvy int, bilinear bool) {
	x, y := x0+(mvx>>3), y0+(mvy>>3)
	fx, fy := mvx&7, mvy&7

	// The source rectangle, with the filter's reach.
	inside := x-2 >= -ref.border && y-2 >= -ref.border &&
		x+w+3 <= ref.w+ref.border && y+h+3 <= ref.h+ref.border
	src := func(sx, sy int) int {
		if inside {
			return int(ref.pix[ref.off(sx, sy)])
		}
		return int(ref.at(sx, sy))
	}

	if fx == 0 && fy == 0 {
		for j := 0; j < h; j++ {
			row := dst.off(x0, y0+j)
			if inside {
				copy(dst.pix[row:row+w], ref.pix[ref.off(x, y+j):])
				continue
			}
			for i := 0; i < w; i++ {
				dst.pix[row+i] = byte(src(x+i, y+j))
			}
		}
		return
	}

	if bilinear {
		var tmp [17 * 16]int
		hf, vf := bilinearFilters[fx], bilinearFilters[fy]
		for j := 0; j < h+1; j++ {
			for i := 0; i < w; i++ {
				tmp[j*w+i] = (src(x+i, y+j)*hf[0] + src(x+i+1, y+j)*hf[1] + 64) >> 7
			}
		}
		for j := 0; j < h; j++ {
			row := dst.off(x0, y0+j)
			for i := 0; i < w; i++ {
				dst.pix[row+i] = byte((tmp[j*w+i]*vf[0] + tmp[(j+1)*w+i]*vf[1] + 64) >> 7)
			}
		}
		return
	}

	// Six-tap, horizontal over the rows the vertical pass needs, then
	// vertical (libvpx vp8_filter_block2d). Each pass rounds and clamps.
	var tmp [21 * 16]int
	hf, vf := &sixtapFilters[fx], &sixtapFilters[fy]
	for j := 0; j < h+5; j++ {
		sy := y + j - 2
		for i := 0; i < w; i++ {
			sx := x + i
			s := src(sx-2, sy)*hf[0] + src(sx-1, sy)*hf[1] + src(sx, sy)*hf[2] +
				src(sx+1, sy)*hf[3] + src(sx+2, sy)*hf[4] + src(sx+3, sy)*hf[5]
			tmp[j*w+i] = int(clamp8((s + 64) >> 7))
		}
	}
	for j := 0; j < h; j++ {
		row := dst.off(x0, y0+j)
		for i := 0; i < w; i++ {
			t := tmp[j*w+i:]
			s := t[0]*vf[0] + t[w]*vf[1] + t[2*w]*vf[2] + t[3*w]*vf[3] + t[4*w]*vf[4] + t[5*w]*vf[5]
			dst.pix[row+i] = clamp8((s + 64) >> 7)
		}
	}
}
