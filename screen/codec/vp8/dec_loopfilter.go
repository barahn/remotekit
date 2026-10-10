// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

// The decoder's loop filter: both of VP8's, normal and simple (RFC 6386 §15),
// as libvpx computes them (vp8/common/loopfilter.c, loopfilter_filters.c).
// The encoder's own filter (loopfilter.go) is the simple one only, which is
// all it emits.

// lfLimits are the thresholds for one filter level.
type lfLimits struct {
	mbLimit, subLimit, interior, hevThresh int
}

// filterLevel returns a macroblock's filter level: the frame's or its
// segment's, adjusted by its reference and mode when deltas are on (libvpx
// vp8_loop_filter_frame_init).
func (d *Decoder) filterLevel(h *frameHeader, mb *decMB) int {
	level := h.filterLevel
	if d.segmentation {
		if d.segmentAbs {
			level = d.segmentFilter[mb.segment]
		} else {
			level += d.segmentFilter[mb.segment]
		}
		level = clampInt(level, 0, 63)
	}
	if !d.lfDeltas {
		return level
	}
	level += d.refLFDeltas[mb.ref]
	switch {
	case mb.ref == refIntra:
		if intraMode(mb.yMode) == B_PRED {
			level += d.modeLFDeltas[0]
		}
	case mb.yMode == modeZero:
		level += d.modeLFDeltas[1]
	case mb.yMode == modeSplit:
		level += d.modeLFDeltas[3]
	default:
		level += d.modeLFDeltas[2]
	}
	return clampInt(level, 0, 63)
}

func limitsFor(level, sharpness int, keyFrame bool) lfLimits {
	interior := level
	if sharpness > 0 {
		interior >>= 1
		if sharpness > 4 {
			interior >>= 1
		}
		if interior > 9-sharpness {
			interior = 9 - sharpness
		}
	}
	if interior < 1 {
		interior = 1
	}
	hev := 0
	switch {
	case level >= 40:
		hev = 2
		if !keyFrame {
			hev = 3
		}
	case level >= 20 && !keyFrame:
		hev = 2
	case level >= 15:
		hev = 1
	}
	return lfLimits{
		mbLimit:   (level+2)*2 + interior,
		subLimit:  level*2 + interior,
		interior:  interior,
		hevThresh: hev,
	}
}

// loopFilter filters the whole reconstructed frame, macroblock by macroblock
// in raster order: the left edge, the inner vertical edges, the top edge, the
// inner horizontal edges. A macroblock with no coefficients that predicts as a
// whole has no inner edges worth filtering.
func (d *Decoder) loopFilter(f *decFrame, h *frameHeader) {
	for mbY := 0; mbY < d.mbH; mbY++ {
		for mbX := 0; mbX < d.mbW; mbX++ {
			mb := &d.mbs[mbY*d.mbW+mbX]
			level := d.filterLevel(h, mb)
			if level == 0 {
				continue
			}
			lim := limitsFor(level, h.sharpness, h.keyFrame)
			inner := mb.hasCoeff || intraMode(mb.yMode) == B_PRED && mb.ref == refIntra || mb.yMode == modeSplit
			x, y := 16*mbX, 16*mbY
			if h.filterSimple {
				d.simpleFilterMB(&f.y, x, y, mbX > 0, mbY > 0, inner, lim)
				continue
			}
			normalFilterMB(f, x, y, mbX > 0, mbY > 0, inner, lim)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// s8 clamps to a signed byte.
func s8(v int) int {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}

// u2s and s2u convert between pixels and the signed values the filters work
// in.
func u2s(v byte) int { return int(v) - 128 }
func s2u(v int) byte { return byte(s8(v) + 128) }

// normalFilterMB applies the normal filter to one macroblock's edges in all
// three planes.
func normalFilterMB(f *decFrame, x, y int, left, top, inner bool, l lfLimits) {
	planes := []struct {
		p    *decPlane
		x, y int
		n    int // the block size in pixels
	}{{&f.y, x, y, 16}, {&f.u, x / 2, y / 2, 8}, {&f.v, x / 2, y / 2, 8}}
	for _, pl := range planes {
		p := pl.p
		if left {
			for j := 0; j < pl.n; j++ {
				mbEdge(p.pix, p.off(pl.x, pl.y+j), 1, l)
			}
		}
		if inner {
			for e := 4; e < pl.n; e += 4 {
				for j := 0; j < pl.n; j++ {
					subEdge(p.pix, p.off(pl.x+e, pl.y+j), 1, l)
				}
			}
		}
		if top {
			for i := 0; i < pl.n; i++ {
				mbEdge(p.pix, p.off(pl.x+i, pl.y), p.stride, l)
			}
		}
		if inner {
			for e := 4; e < pl.n; e += 4 {
				for i := 0; i < pl.n; i++ {
					subEdge(p.pix, p.off(pl.x+i, pl.y+e), p.stride, l)
				}
			}
		}
	}
}

// normalMask reports whether the edge at pix[at], across which step moves, is
// to be filtered with limit as the edge limit.
func normalMask(pix []byte, at, step, limit, interior int) bool {
	p3, p2, p1, p0 := int(pix[at-4*step]), int(pix[at-3*step]), int(pix[at-2*step]), int(pix[at-step])
	q0, q1, q2, q3 := int(pix[at]), int(pix[at+step]), int(pix[at+2*step]), int(pix[at+3*step])
	return abs(p3-p2) <= interior && abs(p2-p1) <= interior && abs(p1-p0) <= interior &&
		abs(q1-q0) <= interior && abs(q2-q1) <= interior && abs(q3-q2) <= interior &&
		abs(p0-q0)*2+abs(p1-q1)/2 <= limit
}

func highVariance(pix []byte, at, step, thresh int) bool {
	return abs(int(pix[at-2*step])-int(pix[at-step])) > thresh ||
		abs(int(pix[at+step])-int(pix[at])) > thresh
}

// subEdge filters one pixel position across an inner edge (libvpx
// vp8_loop_filter_*_edge_c with vp8_filter).
func subEdge(pix []byte, at, step int, l lfLimits) {
	if !normalMask(pix, at, step, l.subLimit, l.interior) {
		return
	}
	hev := highVariance(pix, at, step, l.hevThresh)
	ps1, ps0 := u2s(pix[at-2*step]), u2s(pix[at-step])
	qs0, qs1 := u2s(pix[at]), u2s(pix[at+step])

	a := 0
	if hev {
		a = s8(ps1 - qs1)
	}
	a = s8(a + 3*(qs0-ps0))
	f1 := s8(a+4) >> 3
	f2 := s8(a+3) >> 3
	pix[at] = s2u(qs0 - f1)
	pix[at-step] = s2u(ps0 + f2)
	if !hev {
		a = (f1 + 1) >> 1
		pix[at+step] = s2u(qs1 - a)
		pix[at-2*step] = s2u(ps1 + a)
	}
}

// mbEdge filters one pixel position across a macroblock edge (libvpx
// vp8_mbfilter).
func mbEdge(pix []byte, at, step int, l lfLimits) {
	if !normalMask(pix, at, step, l.mbLimit, l.interior) {
		return
	}
	hev := highVariance(pix, at, step, l.hevThresh)
	ps2, ps1, ps0 := u2s(pix[at-3*step]), u2s(pix[at-2*step]), u2s(pix[at-step])
	qs0, qs1, qs2 := u2s(pix[at]), u2s(pix[at+step]), u2s(pix[at+2*step])

	w := s8(s8(ps1-qs1) + 3*(qs0-ps0))
	if hev {
		f1 := s8(w+4) >> 3
		f2 := s8(w+3) >> 3
		pix[at] = s2u(qs0 - f1)
		pix[at-step] = s2u(ps0 + f2)
		return
	}
	a := s8((27*w + 63) >> 7)
	pix[at] = s2u(qs0 - a)
	pix[at-step] = s2u(ps0 + a)
	a = s8((18*w + 63) >> 7)
	pix[at+step] = s2u(qs1 - a)
	pix[at-2*step] = s2u(ps1 + a)
	a = s8((9*w + 63) >> 7)
	pix[at+2*step] = s2u(qs2 - a)
	pix[at-3*step] = s2u(ps2 + a)
}

// simpleFilterMB applies the simple filter, which touches luma only.
func (d *Decoder) simpleFilterMB(p *decPlane, x, y int, left, top, inner bool, l lfLimits) {
	if left {
		for j := 0; j < 16; j++ {
			simpleEdge(p.pix, p.off(x, y+j), 1, l.mbLimit)
		}
	}
	if inner {
		for e := 4; e < 16; e += 4 {
			for j := 0; j < 16; j++ {
				simpleEdge(p.pix, p.off(x+e, y+j), 1, l.subLimit)
			}
		}
	}
	if top {
		for i := 0; i < 16; i++ {
			simpleEdge(p.pix, p.off(x+i, y), p.stride, l.mbLimit)
		}
	}
	if inner {
		for e := 4; e < 16; e += 4 {
			for i := 0; i < 16; i++ {
				simpleEdge(p.pix, p.off(x+i, y+e), p.stride, l.subLimit)
			}
		}
	}
}

// simpleEdge is libvpx vp8_loop_filter_simple_horizontal_edge_c's inner step.
func simpleEdge(pix []byte, at, step, limit int) {
	p1, p0 := int(pix[at-2*step]), int(pix[at-step])
	q0, q1 := int(pix[at]), int(pix[at+step])
	if abs(p0-q0)*2+abs(p1-q1)/2 > limit {
		return
	}
	ps1, ps0, qs0, qs1 := p1-128, p0-128, q0-128, q1-128
	a := s8(s8(ps1-qs1) + 3*(qs0-ps0))
	f1 := s8(a+4) >> 3
	pix[at] = s2u(qs0 - f1)
	f2 := s8(a+3) >> 3
	pix[at-step] = s2u(ps0 + f2)
}
