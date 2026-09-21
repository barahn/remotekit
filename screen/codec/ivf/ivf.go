// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package ivf writes the IVF container format.
//
// IVF exists in this tree for exactly one reason: `vpxdec`, the libvpx
// reference decoder used as our VP8 conformance oracle, reads IVF and not bare
// VP8 frames. Nothing in the agent's streaming path uses it — WebRTC carries
// VP8 over RTP — so this is deliberately minimal and is not a general-purpose
// container library.
//
// Layout is the de-facto format written by libvpx's ivfenc: a 32-byte file
// header followed by, per frame, a 12-byte frame header and the frame payload.
package ivf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Sizes of the two fixed-length headers, in bytes.
const (
	FileHeaderSize  = 32
	FrameHeaderSize = 12
)

var (
	signature   = [4]byte{'D', 'K', 'I', 'F'}
	fourCCVP8   = [4]byte{'V', 'P', '8', '0'}
	errNoFrames = errors.New("ivf: at least one frame is required")
)

// Config describes the stream-level fields of the IVF file header.
type Config struct {
	// Width and Height are the coded dimensions in pixels.
	Width, Height uint16

	// FPSNumerator and FPSDenominator express the frame rate, e.g. 30 and 1
	// for 30fps. They populate the header's rate and scale fields; libvpx
	// tools use them only for timestamp reporting, not for decoding.
	FPSNumerator, FPSDenominator uint32
}

func (c Config) validate() error {
	switch {
	case c.Width == 0 || c.Height == 0:
		return fmt.Errorf("ivf: dimensions must be non-zero, got %dx%d", c.Width, c.Height)
	case c.FPSNumerator == 0 || c.FPSDenominator == 0:
		return fmt.Errorf("ivf: frame rate must be non-zero, got %d/%d", c.FPSNumerator, c.FPSDenominator)
	}
	return nil
}

// Marshal wraps a sequence of raw VP8 frames in an IVF container.
//
// Frames are timestamped by their index, which is all the reference decoder
// needs; presentation timing is not part of what the conformance oracle checks.
func Marshal(cfg Config, frames [][]byte) ([]byte, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, errNoFrames
	}
	if len(frames) > math.MaxUint32 {
		return nil, fmt.Errorf("ivf: %d frames exceeds the header's uint32 count", len(frames))
	}

	total := FileHeaderSize
	for i, f := range frames {
		if len(f) == 0 {
			return nil, fmt.Errorf("ivf: frame %d is empty", i)
		}
		if len(f) > math.MaxUint32 {
			return nil, fmt.Errorf("ivf: frame %d is %d bytes, exceeding the header's uint32 size", i, len(f))
		}
		total += FrameHeaderSize + len(f)
	}

	buf := make([]byte, FileHeaderSize, total)
	copy(buf[0:4], signature[:])
	binary.LittleEndian.PutUint16(buf[4:6], 0)              // version
	binary.LittleEndian.PutUint16(buf[6:8], FileHeaderSize) // header length
	copy(buf[8:12], fourCCVP8[:])
	binary.LittleEndian.PutUint16(buf[12:14], cfg.Width)
	binary.LittleEndian.PutUint16(buf[14:16], cfg.Height)
	binary.LittleEndian.PutUint32(buf[16:20], cfg.FPSNumerator)
	binary.LittleEndian.PutUint32(buf[20:24], cfg.FPSDenominator)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(len(frames))) // #nosec G115 -- bounded above
	binary.LittleEndian.PutUint32(buf[28:32], 0)                   // reserved

	var frameHdr [FrameHeaderSize]byte
	for i, f := range frames {
		binary.LittleEndian.PutUint32(frameHdr[0:4], uint32(len(f))) // #nosec G115 -- bounded above
		binary.LittleEndian.PutUint64(frameHdr[4:12], uint64(i))     // #nosec G115 -- loop index, non-negative
		buf = append(buf, frameHdr[:]...)
		buf = append(buf, f...)
	}
	return buf, nil
}
