// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package ivf_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/barahn/remotekit/screen/codec/ivf"
)

func validConfig() ivf.Config {
	return ivf.Config{Width: 64, Height: 48, FPSNumerator: 30, FPSDenominator: 1}
}

func TestMarshalHeader(t *testing.T) {
	t.Parallel()

	frames := [][]byte{{0x01, 0x02, 0x03}, {0x04, 0x05}}
	got, err := ivf.Marshal(validConfig(), frames)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	wantLen := ivf.FileHeaderSize + 2*ivf.FrameHeaderSize + 3 + 2
	if len(got) != wantLen {
		t.Fatalf("length %d, want %d", len(got), wantLen)
	}
	if !bytes.Equal(got[0:4], []byte("DKIF")) {
		t.Errorf("signature %q, want DKIF", got[0:4])
	}
	if !bytes.Equal(got[8:12], []byte("VP80")) {
		t.Errorf("fourCC %q, want VP80", got[8:12])
	}
	if v := binary.LittleEndian.Uint16(got[6:8]); v != ivf.FileHeaderSize {
		t.Errorf("header length %d, want %d", v, ivf.FileHeaderSize)
	}
	if v := binary.LittleEndian.Uint16(got[12:14]); v != 64 {
		t.Errorf("width %d, want 64", v)
	}
	if v := binary.LittleEndian.Uint16(got[14:16]); v != 48 {
		t.Errorf("height %d, want 48", v)
	}
	if v := binary.LittleEndian.Uint32(got[24:28]); v != 2 {
		t.Errorf("frame count %d, want 2", v)
	}

	// First frame header: 3-byte payload at timestamp 0.
	off := ivf.FileHeaderSize
	if v := binary.LittleEndian.Uint32(got[off : off+4]); v != 3 {
		t.Errorf("frame 0 size %d, want 3", v)
	}
	if v := binary.LittleEndian.Uint64(got[off+4 : off+12]); v != 0 {
		t.Errorf("frame 0 pts %d, want 0", v)
	}
	if payload := got[off+12 : off+15]; !bytes.Equal(payload, frames[0]) {
		t.Errorf("frame 0 payload %v, want %v", payload, frames[0])
	}

	// Second frame is timestamped by index.
	off += ivf.FrameHeaderSize + 3
	if v := binary.LittleEndian.Uint64(got[off+4 : off+12]); v != 1 {
		t.Errorf("frame 1 pts %d, want 1", v)
	}
}

func TestMarshalRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cfg    ivf.Config
		frames [][]byte
	}{
		{"no frames", validConfig(), nil},
		{"empty frame", validConfig(), [][]byte{{0x01}, {}}},
		{"zero width", ivf.Config{Width: 0, Height: 48, FPSNumerator: 30, FPSDenominator: 1}, [][]byte{{0x01}}},
		{"zero height", ivf.Config{Width: 64, Height: 0, FPSNumerator: 30, FPSDenominator: 1}, [][]byte{{0x01}}},
		{"zero fps numerator", ivf.Config{Width: 64, Height: 48, FPSNumerator: 0, FPSDenominator: 1}, [][]byte{{0x01}}},
		{"zero fps denominator", ivf.Config{Width: 64, Height: 48, FPSNumerator: 30, FPSDenominator: 0}, [][]byte{{0x01}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ivf.Marshal(tt.cfg, tt.frames); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
