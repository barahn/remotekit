// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import (
	"errors"
	"fmt"
	"image"
)

// Decoder decodes a VP8 stream (RFC 6386): key frames and inter frames, with
// golden and altref references, segmentation, multiple token partitions and
// both loop filters. Its output is meant to match libvpx's byte for byte, and
// conformance tests hold it to that against vpxdec.
//
// golang.org/x/image/vp8 decodes key frames only, which is not enough to show
// a screen stream: every frame after the first is an inter frame. This is the
// viewer's half of what Encoder produces (barahn/remotekit#87).
//
// A Decoder is not safe for concurrent use. Frames must be given in stream
// order, each one whole, as webrtc.PeerSession.OnVideoFrame delivers them.
type Decoder struct {
	// width and height are the displayed size; mbW and mbH the size in
	// macroblocks, which the planes are padded to.
	width, height int
	mbW, mbH      int

	// The three references, the frame Decode last returned, and every frame
	// allocated, which newFrame recycles.
	last, golden, altref *decFrame
	shown                *decFrame
	frames               []*decFrame

	// Probabilities that persist from frame to frame (RFC 6386 §9.7-9.11).
	coeffProbs [4][8][3][11]uint8
	yModeProbs [4]uint8
	uvModeProb [3]uint8
	mvProbs    [2][19]uint8

	// Segmentation and loop filter adjustments, which also persist.
	segmentation  bool
	segmentAbs    bool
	segmentQuant  [4]int
	segmentFilter [4]int
	segmentProbs  [3]uint8
	lfDeltas      bool
	refLFDeltas   [4]int
	modeLFDeltas  [4]int

	signBias [4]bool // indexed by reference frame

	// mbs holds every macroblock's modes; segment IDs persist in it.
	mbs []decMB

	// Scratch for the frame being decoded, kept to avoid reallocating.
	intraTop  []uint8 // the sub-block modes along the bottom of the MB row above
	nzTop     []uint8 // non-zero flags above, 9 per macroblock column
	hasFrames bool
}

// Reference frames, in the order the format numbers them.
const (
	refIntra = iota
	refLast
	refGolden
	refAltRef
)

// Inter prediction modes, numbered after the intra ones as libvpx does.
const (
	modeNearest = iota + 5 // after DC_PRED, V_PRED, H_PRED, TM_PRED, B_PRED
	modeNear
	modeZero
	modeNew
	modeSplit
)

// Errors Decode returns. A frame that fails leaves the decoder unchanged
// where it can, but the frames after it predict from references it did not
// update: the caller should wait for the next key frame.
var (
	ErrNotKeyFrame    = errors.New("vp8: the stream does not start with a key frame")
	ErrTruncatedFrame = errors.New("vp8: the frame is truncated")
	ErrInvalidFrame   = errors.New("vp8: the frame is not valid VP8")
)

// maxDecodeDimension and maxDecodePixels bound the size a key frame may
// declare. VP8's own limit is 16383 a side; nobody streams a screen past 8K
// UHD, and a corrupt or hostile header should not make the decoder allocate
// and work through a quarter of a billion pixels.
const (
	maxDecodeDimension = 8192
	maxDecodePixels    = 7680 * 4320
)

// NewDecoder returns a Decoder waiting for its first key frame.
func NewDecoder() *Decoder { return &Decoder{} }

// decMB is what the first partition says about one macroblock, plus what the
// residual adds to it.
type decMB struct {
	yMode    uint8
	uvMode   uint8
	ref      uint8
	segment  uint8
	skip     bool // the bitstream's no-coefficients flag
	hasCoeff bool // whether any block had a token; with skip, gates inner loop filtering
	bModes   [16]intraBMode
	mvs      [16]motionVector // per sub-block; all equal unless split
	split    uint8            // the split partitioning, when yMode is modeSplit
}

// frameHeader is the part of the first partition's header that changes per
// frame and is used after the header is read.
type frameHeader struct {
	keyFrame     bool
	version      int
	show         bool
	filterSimple bool
	filterLevel  int
	sharpness    int
	partitions   int
	qIndex       int
	qDelta       [5]int // y1dc, y2dc, y2ac, uvdc, uvac
	updateSeg    bool
	skipProb     int // -1 when macroblocks carry no skip flag
	probIntra    uint8
	probLast     uint8
	probGolden   uint8
	refreshGold  bool
	refreshAlt   bool
	copyToGold   int
	copyToAlt    int
	refreshLast  bool
	refreshProbs bool
}

// Decode decodes one frame and returns the picture it shows, or nil for a
// frame that is decoded only to serve as a reference (an altref frame, whose
// show_frame flag is clear).
//
// The image is the decoder's own and stays valid only until the next call;
// copy it to keep it.
func (d *Decoder) Decode(data []byte) (*image.YCbCr, error) {
	if len(data) < 3 {
		return nil, ErrTruncatedFrame
	}
	tag := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	var h frameHeader
	h.keyFrame = tag&1 == 0
	h.version = int(tag>>1) & 7
	h.show = tag>>4&1 == 1
	firstSize := int(tag >> 5)
	data = data[3:]

	if h.version > 3 {
		return nil, fmt.Errorf("%w: version %d", ErrInvalidFrame, h.version)
	}
	if h.keyFrame {
		if len(data) < 7 {
			return nil, ErrTruncatedFrame
		}
		if data[0] != 0x9d || data[1] != 0x01 || data[2] != 0x2a {
			return nil, fmt.Errorf("%w: bad start code", ErrInvalidFrame)
		}
		w := int(data[3]) | int(data[4]&0x3f)<<8
		ht := int(data[5]) | int(data[6]&0x3f)<<8
		if w == 0 || ht == 0 || w > maxDecodeDimension || ht > maxDecodeDimension || w*ht > maxDecodePixels {
			return nil, fmt.Errorf("%w: %dx%d", ErrInvalidFrame, w, ht)
		}
		data = data[7:]
		d.resize(w, ht)
		d.resetForKeyFrame()
	} else if !d.hasFrames {
		return nil, ErrNotKeyFrame
	}
	if firstSize > len(data) {
		return nil, ErrTruncatedFrame
	}

	br := newBoolDecoder(data[:firstSize])
	saved, err := d.readHeader(br, &h)
	if err != nil {
		return nil, err
	}
	parts, err := splitPartitions(data[firstSize:], h.partitions)
	if err != nil {
		return nil, err
	}

	cur := d.newFrame()
	d.readModes(br, &h)
	d.decodeMacroblocks(cur, parts, &h)
	if h.filterLevel > 0 {
		d.loopFilter(cur, &h)
	}
	cur.extendBorders()

	d.updateReferences(cur, &h)
	if !h.refreshProbs {
		d.restoreProbs(saved)
	}
	d.hasFrames = true

	if !h.show {
		return nil, nil
	}
	d.shown = cur
	return cur.image(d.width, d.height), nil
}

// resize sets the decoder up for a stream of w x h.
func (d *Decoder) resize(w, h int) {
	mbW, mbH := (w+15)/16, (h+15)/16
	d.width, d.height = w, h
	if mbW == d.mbW && mbH == d.mbH && d.mbs != nil {
		return
	}
	d.mbW, d.mbH = mbW, mbH
	d.mbs = make([]decMB, mbW*mbH)
	d.intraTop = make([]uint8, 4*mbW)
	d.nzTop = make([]uint8, 9*mbW)
	d.last, d.golden, d.altref, d.shown = nil, nil, nil, nil
	d.frames = nil
}

// resetForKeyFrame restores what a key frame resets (libvpx vp8_decode_frame
// and init_frame): the probabilities, segmentation data, loop filter deltas
// and the sign biases.
func (d *Decoder) resetForKeyFrame() {
	d.coeffProbs = DefaultCoeffProbs
	d.yModeProbs = interYModeProb
	d.uvModeProb = interUVModeProb
	d.mvProbs = interMVProbs
	d.segmentAbs = false
	d.segmentQuant = [4]int{}
	d.segmentFilter = [4]int{}
	d.refLFDeltas = [4]int{}
	d.modeLFDeltas = [4]int{}
	d.signBias = [4]bool{}
}

// savedProbs is what a frame with refresh_entropy_probs clear restores
// afterwards.
type savedProbs struct {
	coeff [4][8][3][11]uint8
	yMode [4]uint8
	uv    [3]uint8
	mv    [2][19]uint8
}

func (d *Decoder) restoreProbs(s savedProbs) {
	d.coeffProbs, d.yModeProbs, d.uvModeProb, d.mvProbs = s.coeff, s.yMode, s.uv, s.mv
}

// readHeader reads the frame header from the first partition (RFC 6386
// §9.3-9.11, §19.2) and returns the probabilities to restore after the frame
// if it does not keep its own.
func (d *Decoder) readHeader(br *boolDecoder, h *frameHeader) (savedProbs, error) {
	if h.keyFrame {
		_ = br.readBit() // color space: VP8 defines only one
		_ = br.readBit() // clamping type: the decoder clamps either way
	}

	d.segmentation = br.readBit()
	h.updateSeg = false
	if d.segmentation {
		h.updateSeg = br.readBit()
		updateData := br.readBit()
		if updateData {
			d.segmentAbs = br.readBit()
			for i := range d.segmentQuant {
				d.segmentQuant[i] = br.readOptionalSigned(7)
			}
			for i := range d.segmentFilter {
				d.segmentFilter[i] = br.readOptionalSigned(6)
			}
		}
		if h.updateSeg {
			for i := range d.segmentProbs {
				d.segmentProbs[i] = 255
				if br.readBit() {
					d.segmentProbs[i] = uint8(br.readLiteral(8))
				}
			}
		}
	}

	h.filterSimple = br.readBit()
	h.filterLevel = br.readLiteral(6)
	h.sharpness = br.readLiteral(3)
	d.lfDeltas = br.readBit()
	if d.lfDeltas && br.readBit() {
		for i := range d.refLFDeltas {
			if br.readBit() {
				d.refLFDeltas[i] = br.readSigned(6)
			}
		}
		for i := range d.modeLFDeltas {
			if br.readBit() {
				d.modeLFDeltas[i] = br.readSigned(6)
			}
		}
	}

	h.partitions = 1 << uint(br.readLiteral(2))

	h.qIndex = br.readLiteral(7)
	for i := range h.qDelta {
		h.qDelta[i] = br.readOptionalSigned(4)
	}

	if h.keyFrame {
		h.refreshGold, h.refreshAlt, h.refreshLast = true, true, true
	} else {
		h.refreshGold = br.readBit()
		h.refreshAlt = br.readBit()
		if !h.refreshGold {
			h.copyToGold = br.readLiteral(2)
		}
		if !h.refreshAlt {
			h.copyToAlt = br.readLiteral(2)
		}
		d.signBias[refGolden] = br.readBit()
		d.signBias[refAltRef] = br.readBit()
	}
	h.refreshProbs = br.readBit()
	saved := savedProbs{d.coeffProbs, d.yModeProbs, d.uvModeProb, d.mvProbs}
	if !h.keyFrame {
		h.refreshLast = br.readBit()
	}

	for i := range d.coeffProbs {
		for j := range d.coeffProbs[i] {
			for k := range d.coeffProbs[i][j] {
				for l := range d.coeffProbs[i][j][k] {
					if br.readBool(CoeffProbUpdateProbs[i][j][k][l]) {
						d.coeffProbs[i][j][k][l] = uint8(br.readLiteral(8))
					}
				}
			}
		}
	}

	h.skipProb = -1
	if br.readBit() {
		h.skipProb = br.readLiteral(8)
	}

	if !h.keyFrame {
		h.probIntra = uint8(br.readLiteral(8))
		h.probLast = uint8(br.readLiteral(8))
		h.probGolden = uint8(br.readLiteral(8))
		if br.readBit() {
			for i := range d.yModeProbs {
				d.yModeProbs[i] = uint8(br.readLiteral(8))
			}
		}
		if br.readBit() {
			for i := range d.uvModeProb {
				d.uvModeProb[i] = uint8(br.readLiteral(8))
			}
		}
		for i := range d.mvProbs {
			for j := range d.mvProbs[i] {
				if br.readBool(mvUpdateProbs[i][j]) {
					if v := br.readLiteral(7); v != 0 {
						d.mvProbs[i][j] = uint8(v << 1)
					} else {
						d.mvProbs[i][j] = 1
					}
				}
			}
		}
	}
	return saved, nil
}

// splitPartitions cuts the token partitions out of what follows the first
// partition: n-1 three-byte little-endian sizes, then the partitions, the
// last taking whatever remains.
func splitPartitions(data []byte, n int) ([][]byte, error) {
	sizes := 3 * (n - 1)
	if len(data) < sizes {
		return nil, ErrTruncatedFrame
	}
	parts := make([][]byte, n)
	rest := data[sizes:]
	for i := 0; i < n-1; i++ {
		size := int(data[3*i]) | int(data[3*i+1])<<8 | int(data[3*i+2])<<16
		if size > len(rest) {
			return nil, ErrTruncatedFrame
		}
		parts[i], rest = rest[:size], rest[size:]
	}
	parts[n-1] = rest
	return parts, nil
}

// updateReferences applies the frame's reference updates in libvpx's order
// (swap_frame_buffers): altref's copy first, then golden's -- which, copying
// "from altref", sees the altref just set -- then the refreshes.
func (d *Decoder) updateReferences(cur *decFrame, h *frameHeader) {
	switch h.copyToAlt {
	case 1:
		d.altref = d.last
	case 2:
		d.altref = d.golden
	}
	switch h.copyToGold {
	case 1:
		d.golden = d.last
	case 2:
		d.golden = d.altref
	}
	if h.refreshGold {
		d.golden = cur
	}
	if h.refreshAlt {
		d.altref = cur
	}
	if h.refreshLast {
		d.last = cur
	}
}
