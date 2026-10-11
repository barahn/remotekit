// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package vp8_test

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barahn/remotekit/screen/codec/vp8"
)

// TestDecoderMatchesLibvpxTestVectors decodes libvpx's own conformance
// vectors (testdata/libvpx, see its README) and checks every shown frame
// against the MD5 libvpx publishes for it. Unlike TestDecoderMatchesLibvpx,
// these streams come from libvpx's encoder, so they exercise what this
// package's encoder never emits: SPLITMV, the normal loop filter,
// segmentation, altref and hidden frames, sign bias, odd sizes.
func TestDecoderMatchesLibvpxTestVectors(t *testing.T) {
	vectors, err := filepath.Glob(filepath.Join("testdata", "libvpx", "*.ivf"))
	if err != nil || len(vectors) == 0 {
		t.Fatalf("no test vectors found: %v", err)
	}
	for _, path := range vectors {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := readMD5s(t, path+".md5")
			dec := vp8.NewDecoder()
			shown := 0
			for i, f := range splitIVF(t, data) {
				img, err := dec.Decode(f)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if img == nil {
					continue // a hidden frame: decoded, not shown
				}
				if shown >= len(want) {
					t.Fatalf("frame %d: more frames shown than the %d libvpx shows", i, len(want))
				}
				if got := i420MD5(img); got != want[shown] {
					t.Fatalf("frame %d (shown %d): MD5 %s, want %s", i, shown+1, got, want[shown])
				}
				shown++
			}
			if shown != len(want) {
				t.Fatalf("showed %d frames, libvpx shows %d", shown, len(want))
			}
		})
	}
}

// i420MD5 hashes a picture as vpxdec --i420 writes it: the visible luma, then
// each chroma plane at half the size, rounded up.
func i420MD5(img *image.YCbCr) string {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	cw, ch := (w+1)/2, (h+1)/2
	sum := md5.New()
	for y := 0; y < h; y++ {
		sum.Write(img.Y[y*img.YStride : y*img.YStride+w])
	}
	for _, p := range [][]byte{img.Cb, img.Cr} {
		for y := 0; y < ch; y++ {
			sum.Write(p[y*img.CStride : y*img.CStride+cw])
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func readMD5s(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var sums []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fields := strings.Fields(s.Text()); len(fields) > 0 {
			sums = append(sums, fields[0])
		}
	}
	return sums
}

// splitIVF returns an IVF file's frames.
func splitIVF(t *testing.T, data []byte) [][]byte {
	t.Helper()
	if len(data) < 32 || string(data[:4]) != "DKIF" {
		t.Fatal("not an IVF file")
	}
	var frames [][]byte
	for p := 32; p+12 <= len(data); {
		n := int(data[p]) | int(data[p+1])<<8 | int(data[p+2])<<16 | int(data[p+3])<<24
		p += 12
		if n > len(data)-p {
			t.Fatal("truncated IVF frame")
		}
		frames = append(frames, data[p:p+n])
		p += n
	}
	return frames
}
