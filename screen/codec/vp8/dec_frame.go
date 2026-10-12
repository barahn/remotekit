// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import "image"

// decBorder is how far each plane extends past the macroblock grid, in luma
// pixels. Inter prediction reads up to a block plus the six-tap filter's
// reach outside the frame; within the border it reads the plane directly, and
// past it falls back to clamping coordinates, which gives the same pixels as
// an infinitely extended border.
const decBorder = 48

// decPlane is one plane of a frame, with a border on every side.
type decPlane struct {
	pix    []byte
	stride int
	w, h   int // the macroblock-aligned size, border excluded
	border int
}

func newDecPlane(w, h, border int) decPlane {
	stride := w + 2*border
	return decPlane{
		pix:    make([]byte, stride*(h+2*border)),
		stride: stride,
		w:      w,
		h:      h,
		border: border,
	}
}

// off returns the index of pixel (x, y), where (0, 0) is the plane's first
// pixel inside the border.
func (p *decPlane) off(x, y int) int { return (y+p.border)*p.stride + x + p.border }

// at returns pixel (x, y) with the coordinates clamped to the plane, which is
// what reading an infinitely extended border gives.
func (p *decPlane) at(x, y int) byte {
	if x < 0 {
		x = 0
	} else if x >= p.w {
		x = p.w - 1
	}
	if y < 0 {
		y = 0
	} else if y >= p.h {
		y = p.h - 1
	}
	return p.pix[p.off(x, y)]
}

// extend replicates the plane's edges into its border.
func (p *decPlane) extend() {
	for y := 0; y < p.h; y++ {
		row := p.off(0, y)
		l, r := p.pix[row], p.pix[row+p.w-1]
		for i := 1; i <= p.border; i++ {
			p.pix[row-i] = l
			p.pix[row+p.w-1+i] = r
		}
	}
	top := p.pix[p.off(-p.border, 0) : p.off(-p.border, 0)+p.stride]
	bottom := p.pix[p.off(-p.border, p.h-1) : p.off(-p.border, p.h-1)+p.stride]
	for i := 1; i <= p.border; i++ {
		copy(p.pix[p.off(-p.border, -i):], top)
		copy(p.pix[p.off(-p.border, p.h-1+i):], bottom)
	}
}

// prepareIntraEdges writes the values intra prediction reads outside the
// frame (libvpx vp8_setup_intra_recon): 127 along the row above, from the
// corner to four pixels past the right edge, and 129 down the column to the
// left.
func (p *decPlane) prepareIntraEdges() {
	above := p.off(-1, -1)
	for i := 0; i < p.w+5; i++ {
		p.pix[above+i] = 127
	}
	for y := 0; y < p.h; y++ {
		p.pix[p.off(-1, y)] = 129
	}
}

// decFrame is a decoded frame: three planes at macroblock-aligned size.
type decFrame struct {
	y, u, v decPlane
}

func newDecFrame(mbW, mbH int) *decFrame {
	return &decFrame{
		y: newDecPlane(16*mbW, 16*mbH, decBorder),
		u: newDecPlane(8*mbW, 8*mbH, decBorder/2),
		v: newDecPlane(8*mbW, 8*mbH, decBorder/2),
	}
}

func (f *decFrame) extendBorders() {
	f.y.extend()
	f.u.extend()
	f.v.extend()
}

// image returns the frame's visible w x h as a YCbCr image sharing its
// pixels.
func (f *decFrame) image(w, h int) *image.YCbCr {
	return &image.YCbCr{
		Y:              f.y.pix[f.y.off(0, 0):],
		Cb:             f.u.pix[f.u.off(0, 0):],
		Cr:             f.v.pix[f.v.off(0, 0):],
		YStride:        f.y.stride,
		CStride:        f.u.stride,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, w, h),
	}
}

// newFrame returns a frame to decode into, reusing one that no reference
// holds and that is not the picture the last Decode returned, whose caller may
// still be reading it. At most five frames are ever allocated.
func (d *Decoder) newFrame() *decFrame {
	var f *decFrame
	for _, c := range d.frames {
		if c != d.last && c != d.golden && c != d.altref && c != d.shown {
			f = c
			break
		}
	}
	if f == nil {
		f = newDecFrame(d.mbW, d.mbH)
		d.frames = append(d.frames, f)
	}
	f.y.prepareIntraEdges()
	f.u.prepareIntraEdges()
	f.v.prepareIntraEdges()
	return f
}
