// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"context"
	"errors"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/permission"
)

// fakeMacBackend stands in for ScreenCaptureKit so the macOS capturer's logic
// runs on every platform's CI.
type fakeMacBackend struct {
	mu       sync.Mutex
	list     []macDisplay
	status   permission.Status
	startErr error
	started  []*fakeMacStream
}

func (b *fakeMacBackend) displays() ([]macDisplay, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]macDisplay(nil), b.list...), nil
}

func (b *fakeMacBackend) screenRecording() permission.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

func (b *fakeMacBackend) start(d macDisplay, _ CaptureConfig) (macStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startErr != nil {
		return nil, b.startErr
	}
	s := &fakeMacStream{display: d}
	b.started = append(b.started, s)
	return s, nil
}

func (b *fakeMacBackend) streams() []*fakeMacStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*fakeMacStream(nil), b.started...)
}

type fakeMacStream struct {
	mu      sync.Mutex
	display macDisplay
	stopped bool
	err     error
}

func (s *fakeMacStream) latest() (*image.RGBA, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	img := image.NewRGBA(image.Rect(0, 0, s.display.pixelW, s.display.pixelH))
	img.Pix[0] = byte(s.display.id)
	return img, nil
}

func (s *fakeMacStream) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
}

func (s *fakeMacStream) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *fakeMacStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// A Retina laptop (1440x900 points, 2880x1800 pixels) with an external
// 1920x1080 monitor to its left. CoreGraphics happens to list the external one
// first; the main display must still come first.
func twoMacDisplays() []macDisplay {
	return []macDisplay{
		{id: 7, bounds: image.Rect(-1920, 0, 0, 1080), pixelW: 1920, pixelH: 1080},
		{id: 3, bounds: image.Rect(0, 0, 1440, 900), pixelW: 2880, pixelH: 1800, main: true},
	}
}

func newTestMacCapturer(b *fakeMacBackend) *macCapturer {
	cfg := DefaultConfig()
	cfg.TargetFPS = 100
	return newMacCapturer(b, cfg)
}

func nextFrame(t *testing.T, c Capturer) *Frame {
	t.Helper()
	select {
	case f, ok := <-c.Frames():
		if !ok {
			t.Fatal("Frames closed")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame within 2s")
	}
	return nil
}

func TestMacDisplaysPutTheMainDisplayFirst(t *testing.T) {
	c := newTestMacCapturer(&fakeMacBackend{list: twoMacDisplays(), status: permission.Granted})

	got, err := c.Displays()
	if err != nil {
		t.Fatal(err)
	}
	want := []Display{
		{Index: 0, Name: "Display 1 (2880x1800)", Bounds: image.Rect(0, 0, 1440, 900), Primary: true},
		{Index: 1, Name: "Display 2 (1920x1080)", Bounds: image.Rect(-1920, 0, 0, 1080)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d displays, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("display %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Displays needs no Screen Recording: enumerating monitors is not capturing
// them, and a product can show the monitors while it explains the permission.
func TestMacDisplaysWorkWithoutScreenRecording(t *testing.T) {
	c := newTestMacCapturer(&fakeMacBackend{list: twoMacDisplays(), status: permission.NotGranted})
	got, err := c.Displays()
	if err != nil || len(got) != 2 {
		t.Fatalf("Displays() = %v, %v; want two displays", got, err)
	}
}

// Without Screen Recording, Start must say so in a way a product can tell
// apart from "no backend": it asks the person for the permission in one case
// and gives up in the other. It must not try ScreenCaptureKit either: without
// the permission each attempt raises the system's dialog again, and the runner
// tries once per offer.
func TestMacStartWithoutScreenRecordingNamesThePermission(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.NotGranted}
	c := newTestMacCapturer(b)

	err := c.Start(context.Background())
	if err == nil {
		c.Stop()
		t.Fatal("Start succeeded without Screen Recording")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("errors.Is(%v, ErrPermissionDenied) = false", err)
	}
	var ng *permission.NotGrantedError
	if !errors.As(err, &ng) || ng.Permission != permission.ScreenRecording {
		t.Errorf("error %v does not name Screen Recording", err)
	}
	if errors.Is(err, ErrCaptureNotReady) {
		t.Errorf("a missing permission reads as a missing backend: %v", err)
	}
	if n := len(b.streams()); n != 0 {
		t.Errorf("%d streams started without the permission", n)
	}
	if c.Frames() != nil {
		t.Error("Frames is not nil after a refused Start")
	}
}

// Revoking Screen Recording mid-session ends capture: Frames closes and the
// stream is not restarted, because every ScreenCaptureKit call made without
// the permission puts the system's permission dialog in front of the person
// again.
func TestMacRevokedScreenRecordingEndsCapture(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)
	c.restartEvery = time.Millisecond
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	nextFrame(t, c)

	b.streams()[0].fail(screenRecordingError())
	drainClosed(t, c.Frames())
	if n := len(b.streams()); n != 1 {
		t.Fatalf("%d streams started; a revoked permission must not be retried", n)
	}
	if !b.streams()[0].isStopped() {
		t.Error("the refused stream was not released")
	}
}

// ScreenCaptureKit can refuse even when the preflight said yes, for instance
// when the grant was revoked in between; that refusal must come back intact.
func TestMacStartPassesOnTheBackendsRefusal(t *testing.T) {
	refusal := screenRecordingError()
	c := newTestMacCapturer(&fakeMacBackend{list: twoMacDisplays(), status: permission.Granted, startErr: refusal})
	err := c.Start(context.Background())
	if !errors.Is(err, permission.ErrNotGranted) {
		t.Fatalf("Start() = %v, want the permission error", err)
	}
}

func TestMacCaptureDeliversThePrimaryDisplay(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	f := nextFrame(t, c)
	// Image in pixels, Bounds in points: input is injected in points, the
	// encoder takes pixels.
	if got := f.Image.Bounds(); got != image.Rect(0, 0, 2880, 1800) {
		t.Errorf("Image bounds = %v, want the 2880x1800 pixels", got)
	}
	if f.Bounds != image.Rect(0, 0, 1440, 900) {
		t.Errorf("Bounds = %v, want the 1440x900 points", f.Bounds)
	}
	if f.DisplayIndex != 0 {
		t.Errorf("DisplayIndex = %d, want 0", f.DisplayIndex)
	}
	if f.SequenceNum == 0 {
		t.Error("SequenceNum is 0")
	}
	if f.CapturedAt.IsZero() {
		t.Error("CapturedAt is zero")
	}
	if f2 := nextFrame(t, c); f2.SequenceNum <= f.SequenceNum {
		t.Errorf("SequenceNum went from %d to %d", f.SequenceNum, f2.SequenceNum)
	}
}

func TestMacStartTwice(t *testing.T) {
	c := newTestMacCapturer(&fakeMacBackend{list: twoMacDisplays(), status: permission.Granted})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if err := c.Start(context.Background()); err != ErrAlreadyStarted {
		t.Fatalf("second Start() = %v, want ErrAlreadyStarted", err)
	}
}

func TestMacSetDisplay(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)

	for _, bad := range []int{-1, 2, 99} {
		if err := c.SetDisplay(bad); err != ErrDisplayNotFound {
			t.Errorf("SetDisplay(%d) = %v, want ErrDisplayNotFound", bad, err)
		}
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	nextFrame(t, c)

	if err := c.SetDisplay(1); err != nil {
		t.Fatalf("SetDisplay(1) = %v", err)
	}
	// Frames already buffered may still be the old display's.
	deadline := time.Now().Add(2 * time.Second)
	for {
		f := nextFrame(t, c)
		if f.DisplayIndex == 1 {
			if f.Bounds != image.Rect(-1920, 0, 0, 1080) || f.Image.Bounds().Dx() != 1920 {
				t.Fatalf("display 1 frame has Bounds %v and image %v", f.Bounds, f.Image.Bounds())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no frame from display 1 after SetDisplay(1)")
		}
	}
	streams := b.streams()
	if len(streams) != 2 || !streams[0].isStopped() || streams[1].display.id != 7 {
		t.Fatalf("switching displays should stop the first stream and start one on display 7")
	}
}

func TestMacStopClosesFramesAndCanRestart(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	frames := c.Frames()
	c.Stop()
	// Stop has returned: the stream is released and Frames closed already.
	if !b.streams()[0].isStopped() {
		t.Error("Stop left the stream running")
	}
	drainClosed(t, frames)
	c.Stop() // must be safe

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start after Stop = %v", err)
	}
	nextFrame(t, c)
	c.Close()
	drainClosed(t, c.Frames())
}

func TestMacContextCancelEndsCapture(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	drainClosed(t, c.Frames())
	if !b.streams()[0].isStopped() {
		t.Error("cancelling the context left the stream running")
	}
}

// A stream the system stopped -- the display went away, the permission was
// revoked -- is restarted rather than leaving the viewer on a frozen frame.
func TestMacStoppedStreamIsRestarted(t *testing.T) {
	b := &fakeMacBackend{list: twoMacDisplays(), status: permission.Granted}
	c := newTestMacCapturer(b)
	c.restartEvery = 10 * time.Millisecond
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	nextFrame(t, c)

	b.streams()[0].fail(errors.New("stream stopped"))

	deadline := time.Now().Add(2 * time.Second)
	for len(b.streams()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the stopped stream was not restarted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !b.streams()[0].isStopped() {
		t.Error("the failed stream was not released")
	}
	nextFrame(t, c)
}

func drainClosed(t *testing.T, frames <-chan *Frame) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("Frames was not closed")
		}
	}
}

func TestCopyBGRAToRGBASkipsRowPadding(t *testing.T) {
	// 2x2 BGRA with 4 bytes of padding per row, as CoreVideo pads rows.
	src := []byte{
		1, 2, 3, 0, 4, 5, 6, 0, 0xEE, 0xEE, 0xEE, 0xEE,
		7, 8, 9, 0, 10, 11, 12, 0, 0xEE, 0xEE, 0xEE, 0xEE,
	}
	dst := make([]byte, 2*2*4)
	copyBGRAToRGBA(dst, src, 2, 2, 12)
	want := []byte{3, 2, 1, 255, 6, 5, 4, 255, 9, 8, 7, 255, 12, 11, 10, 255}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("dst = %v, want %v", dst, want)
		}
	}
}
