// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8

import "math/bits"

// boolDecoder reads the boolean-entropy-coded data every VP8 partition is made
// of (RFC 6386 §7). It is the inverse of boolEncoder.
//
// It keeps up to 64 bits of the partition in hand and renormalises in one
// shift, the way libvpx and libwebp do, rather than byte by byte: decoding a
// frame is mostly this function, millions of times.
//
// Reading past the end of the data reads zeros, as libvpx does. A truncated or
// corrupt partition therefore decodes to garbage rather than failing, and the
// decoder above it must bound everything it derives from what it reads.
type boolDecoder struct {
	data  []byte
	pos   int
	value uint64 // unread bits; the window being decoded starts at bit `bits`
	rng   uint32 // the current range minus one, in [127, 254] between reads
	bits  int    // the window's position in value; negative when it needs bytes
}

func newBoolDecoder(data []byte) *boolDecoder {
	d := &boolDecoder{data: data, rng: 255 - 1, bits: -8}
	d.load()
	return d
}

// load appends bytes to value: seven at once while there are that many, then
// one at a time, then zeros past the end.
func (d *boolDecoder) load() {
	if d.pos+8 <= len(d.data) {
		b := d.data[d.pos : d.pos+8]
		in := uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
			uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
		d.value = d.value<<56 | in>>8
		d.pos += 7
		d.bits += 56
		return
	}
	var next uint64
	if d.pos < len(d.data) {
		next = uint64(d.data[d.pos])
	}
	d.pos++
	d.value = d.value<<8 | next
	d.bits += 8
}

// readBool reads one boolean whose probability of being false is prob/256.
func (d *boolDecoder) readBool(prob uint8) bool {
	if d.bits < 0 {
		d.load()
	}
	pos := uint(d.bits)
	split := (d.rng * uint32(prob)) >> 8 // the format's split, minus one
	var r uint32                         // the new range
	bit := uint32(d.value>>pos) > split
	if bit {
		r = d.rng - split
		d.value -= uint64(split+1) << pos
	} else {
		r = split + 1
	}
	// Renormalise: shift until the range is back in [128, 255].
	shift := 8 - bits.Len32(r)
	d.rng = r<<uint(shift) - 1
	d.bits -= shift
	return bit
}

// readBit reads one boolean at even odds.
func (d *boolDecoder) readBit() bool { return d.readBool(128) }

// readLiteral reads an n-bit unsigned value, most significant bit first.
func (d *boolDecoder) readLiteral(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v <<= 1
		if d.readBit() {
			v |= 1
		}
	}
	return v
}

// readSigned reads an n-bit magnitude followed by a sign bit, the way frame
// header deltas are coded.
func (d *boolDecoder) readSigned(n int) int {
	v := d.readLiteral(n)
	if d.readBit() {
		return -v
	}
	return v
}

// readOptionalSigned reads a flag and, when it is set, a signed value.
func (d *boolDecoder) readOptionalSigned(n int) int {
	if !d.readBit() {
		return 0
	}
	return d.readSigned(n)
}

// readTree reads a value coded with tree t and probabilities p, in the
// representation libvpx uses: a positive entry is the index of the next node
// pair, and a leaf v is stored as -v.
func (d *boolDecoder) readTree(t []int8, p []uint8) int {
	i := 0
	for {
		b := 0
		if d.readBool(p[i>>1]) {
			b = 1
		}
		i = int(t[i+b])
		if i <= 0 {
			return -i
		}
	}
}
