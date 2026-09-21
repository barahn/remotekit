// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package vp8check runs the libvpx reference decoder (`vpxdec`) as an
// out-of-band oracle over VP8 encoder output.
//
// # Why an external decoder
//
// golang.org/x/image/vp8 decodes key frames only — it returns
// "vp8: Golden / AltRef frames are not implemented" for anything else — so it
// cannot validate the inter-frame path, which is where the entire bandwidth
// win of screen sharing lives. Using it as the only oracle is precisely how the
// upstream encoder we build on ended up with an inter-frame path that no test
// ever decoded (opd-ai/vp8, GAPS.md §2). Key frames stay on the fast in-process
// decoder; everything else is checked here.
//
// # Why this does not breach the zero-CGo premise
//
// libvpx runs as a test-time subprocess. It is never linked into the agent, and
// nothing here is reachable from a production build. The premise protects the
// agent's address space, not the CI pipeline.
package vp8check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ToolName is the reference decoder binary this package shells out to.
// On Debian and Ubuntu it ships in the `vpx-tools` package.
const ToolName = "vpxdec"

// RequiredEnv, when set to a non-empty value, means a missing reference decoder
// must fail the run rather than skip it. CI sets it. A conformance gate that
// silently skips is not a gate.
const RequiredEnv = "VP8_CONFORMANCE_REQUIRED"

// decodeTimeout bounds the subprocess so a decoder that hangs on a malformed
// stream cannot hold a CI runner open.
const decodeTimeout = 60 * time.Second

// ErrToolMissing reports that the reference decoder is not installed.
var ErrToolMissing = errors.New("vp8check: " + ToolName + " not found in PATH")

// DecodeError is returned when the reference decoder rejects a stream. Its
// Stderr carries the decoder's own diagnostic, which is the useful part when a
// bitstream bug is being chased.
type DecodeError struct {
	Err    error
	Stderr string
}

func (e *DecodeError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("vp8check: %s rejected the stream: %v", ToolName, e.Err)
	}
	return fmt.Sprintf("vp8check: %s rejected the stream: %v: %s", ToolName, e.Err, e.Stderr)
}

func (e *DecodeError) Unwrap() error { return e.Err }

// Frame is one decoded picture in planar I420, with the chroma planes at half
// resolution in each dimension.
type Frame struct {
	Y, U, V       []byte
	Width, Height int
}

// Availability locates the reference decoder and reports whether its absence is
// fatal for this run.
//
// Callers in tests are expected to skip when required is false and fail when it
// is true; keeping that decision here rather than in a testing helper keeps the
// `testing` package out of a non-test dependency.
func Availability() (path string, required bool, err error) {
	required = os.Getenv(RequiredEnv) != ""
	path, err = exec.LookPath(ToolName)
	if err != nil {
		return "", required, fmt.Errorf("%w: %w", ErrToolMissing, err)
	}
	return path, required, nil
}

// Decode runs the reference decoder over an IVF stream and returns the decoded
// frames. A non-nil error means the stream is not conformant — that is the
// signal the oracle exists to produce.
//
// width and height are the expected coded dimensions; they determine how the
// decoder's raw output is split into frames, so a stream that decodes to
// different dimensions is reported as an error rather than silently misparsed.
func Decode(toolPath string, ivfData []byte, width, height int) ([]Frame, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("vp8check: invalid dimensions %dx%d", width, height)
	}
	if width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("vp8check: I420 requires even dimensions, got %dx%d", width, height)
	}

	dir, err := os.MkdirTemp("", "vp8check-")
	if err != nil {
		return nil, fmt.Errorf("vp8check: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	inPath := filepath.Join(dir, "stream.ivf")
	outPath := filepath.Join(dir, "decoded.i420")
	if err := os.WriteFile(inPath, ivfData, 0o600); err != nil {
		return nil, fmt.Errorf("vp8check: write stream: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), decodeTimeout)
	defer cancel()

	// #nosec G204 -- toolPath is resolved by exec.LookPath from the fixed
	// constant ToolName; every other argument is a literal or a path this
	// function just created inside its own temp dir.
	cmd := exec.CommandContext(ctx, toolPath, "--codec=vp8", "--i420", "-o", outPath, inPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &DecodeError{Err: err, Stderr: stderr.String()}
	}

	raw, err := os.ReadFile(outPath) // #nosec G304 -- path constructed above inside our temp dir
	if err != nil {
		return nil, fmt.Errorf("vp8check: read decoded output: %w", err)
	}
	if len(raw) == 0 {
		return nil, &DecodeError{
			Err:    errors.New("decoder produced no output"),
			Stderr: stderr.String(),
		}
	}

	frameSize := width * height * 3 / 2
	if len(raw)%frameSize != 0 {
		return nil, &DecodeError{
			Err: fmt.Errorf("decoded %d bytes, not a whole number of %dx%d I420 frames (%d bytes each) — "+
				"the stream most likely codes different dimensions", len(raw), width, height, frameSize),
			Stderr: stderr.String(),
		}
	}

	lumaSize := width * height
	chromaSize := lumaSize / 4
	frames := make([]Frame, 0, len(raw)/frameSize)
	for off := 0; off < len(raw); off += frameSize {
		f := raw[off : off+frameSize]
		frames = append(frames, Frame{
			Y:      f[:lumaSize],
			U:      f[lumaSize : lumaSize+chromaSize],
			V:      f[lumaSize+chromaSize:],
			Width:  width,
			Height: height,
		})
	}
	return frames, nil
}

// PSNR returns the peak signal-to-noise ratio in dB between two equally sized
// 8-bit planes. Identical planes return math.Inf(1); callers should treat that
// as a pass rather than compare it numerically.
func PSNR(a, b []byte) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vp8check: plane lengths differ: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, errors.New("vp8check: empty plane")
	}
	var sum float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	mse := sum / float64(len(a))
	if mse == 0 {
		return math.Inf(1), nil
	}
	return 10 * math.Log10(255*255/mse), nil
}
