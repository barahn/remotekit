// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"sync"
	"time"

	"github.com/barahn/remotekit/permission"
)

// This file is the macOS capturer's logic: which display, when to start and
// stop, what a frame carries. It has no build tag so that it is tested on
// every platform's CI against a fake backend; capture_darwin.go supplies the
// real one, built on ScreenCaptureKit.

// macDisplay is one display as macOS reports it.
type macDisplay struct {
	id uint32 // CGDirectDisplayID
	// bounds is the display in global coordinates, in points: the space
	// CoreGraphics places windows and posts input events in. A Retina display
	// has more pixels than points.
	bounds image.Rectangle
	// pixelW and pixelH are the display's native size in pixels, which is
	// what is captured.
	pixelW, pixelH int
	main           bool
}

// macBackend is what the capturer needs from the system.
type macBackend interface {
	// displays lists the active displays. It needs no permission.
	displays() ([]macDisplay, error)
	// screenRecording reports the Screen Recording permission.
	screenRecording() permission.Status
	// start begins capturing d at its native pixel size.
	start(d macDisplay, cfg CaptureConfig) (macStream, error)
}

// macStream is one running capture of one display.
type macStream interface {
	// latest returns the newest captured picture, or nil before the first
	// one. It returns an error once the system has stopped the stream.
	latest() (*image.RGBA, error)
	// stop ends the stream and releases it. It is called exactly once.
	stop()
}

// screenRecordingError is what capture returns without Screen Recording. It
// matches ErrPermissionDenied and permission.ErrNotGranted, and errors.As
// finds a *permission.NotGrantedError naming the permission; it does not match
// ErrCaptureNotReady, which means there is no backend at all.
func screenRecordingError() error {
	return permission.Missing(ErrPermissionDenied, permission.ScreenRecording)
}

// orderMacDisplays puts the main display first and keeps the others in the
// order the system listed them, so index 0 is always the primary display, as
// DisplayIndex -1 promises.
func orderMacDisplays(list []macDisplay) []macDisplay {
	out := append([]macDisplay(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].main && !out[j].main })
	return out
}

func macDisplaysToDisplays(list []macDisplay) []Display {
	out := make([]Display, len(list))
	for i, d := range list {
		out[i] = Display{
			Index:   i,
			Name:    fmt.Sprintf("Display %d (%dx%d)", i+1, d.pixelW, d.pixelH),
			Bounds:  d.bounds,
			Primary: d.main,
		}
	}
	return out
}

type macCapturer struct {
	backend macBackend

	// restartEvery is how long to wait before restarting a stream the system
	// stopped.
	restartEvery time.Duration

	mu      sync.Mutex
	config  CaptureConfig
	frames  chan *Frame
	running bool
	cancel  context.CancelFunc
	done    chan struct{} // closed when the capture loop has ended
	stream  macStream
	display macDisplay
	index   int
	seq     uint64
}

func newMacCapturer(b macBackend, config CaptureConfig) *macCapturer {
	if config.FrameBufferSize <= 0 {
		config.FrameBufferSize = 2
	}
	if config.TargetFPS <= 0 {
		config.TargetFPS = 30
	}
	return &macCapturer{backend: b, config: config, restartEvery: time.Second}
}

func (c *macCapturer) orderedDisplays() ([]macDisplay, error) {
	list, err := c.backend.displays()
	if err != nil {
		return nil, err
	}
	return orderMacDisplays(list), nil
}

func (c *macCapturer) Displays() ([]Display, error) {
	list, err := c.orderedDisplays()
	if err != nil {
		return nil, err
	}
	return macDisplaysToDisplays(list), nil
}

func (c *macCapturer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return ErrAlreadyStarted
	}

	// The preflight decides, and ScreenCaptureKit is not asked without it:
	// every ScreenCaptureKit call made without the permission raises the
	// system's dialog again, and the runner tries once per offer. The
	// preflight's answer is fixed for the life of the process, so a grant
	// made while it runs takes effect once it is relaunched.
	switch c.backend.screenRecording() {
	case permission.Granted:
	case permission.NotGranted:
		return screenRecordingError()
	default:
		return fmt.Errorf("%w: cannot read the Screen Recording permission", ErrCaptureNotReady)
	}

	list, err := c.orderedDisplays()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCaptureNotReady, err)
	}
	if len(list) == 0 {
		return fmt.Errorf("%w: no active display", ErrCaptureNotReady)
	}
	idx := c.config.DisplayIndex
	if idx < 0 || idx >= len(list) {
		idx = 0 // the main display, see orderMacDisplays
	}
	stream, err := c.backend.start(list[idx], c.config)
	if err != nil {
		return err
	}

	captureCtx, cancel := context.WithCancel(ctx)
	c.stream, c.display, c.index = stream, list[idx], idx
	c.frames = make(chan *Frame, c.config.FrameBufferSize)
	c.cancel = cancel
	c.done = make(chan struct{})
	c.running = true
	go c.loop(captureCtx, c.frames, c.done, time.Second/time.Duration(c.config.TargetFPS))
	return nil
}

func (c *macCapturer) loop(ctx context.Context, frames chan *Frame, done chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer func() {
		c.mu.Lock()
		if c.stream != nil {
			c.stream.stop()
			c.stream = nil
		}
		c.running = false
		c.mu.Unlock()
		close(frames)
		close(done)
	}()

	var failedAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		c.mu.Lock()
		var img *image.RGBA
		var err error
		if c.stream != nil {
			img, err = c.stream.latest()
		}
		if errors.Is(err, permission.ErrNotGranted) {
			// Screen Recording was revoked. Retrying would put the system's
			// permission dialog in front of the person on every attempt, so
			// capture ends here and Frames closes.
			c.mu.Unlock()
			return
		}
		if c.stream == nil || err != nil {
			// The system stopped the stream. Restart it after a pause, on the
			// same display, rather than leave the viewer on a frozen picture.
			if c.stream != nil {
				c.stream.stop()
				c.stream = nil
				failedAt = time.Now()
			}
			if time.Since(failedAt) >= c.restartEvery {
				failedAt = time.Now()
				if s, startErr := c.backend.start(c.display, c.config); startErr == nil {
					c.stream = s
				}
			}
			c.mu.Unlock()
			continue
		}
		if img == nil {
			c.mu.Unlock()
			continue
		}
		c.seq++
		frame := &Frame{
			Image: img,
			// The display in global coordinates, in points: the space the
			// injector aims at. The image is in pixels.
			Bounds:       c.display.bounds,
			DisplayIndex: c.index,
			CapturedAt:   time.Now(),
			SequenceNum:  c.seq,
		}
		c.mu.Unlock()

		select {
		case frames <- frame:
		default:
			// Dropped for a slow consumer.
		}
	}
}

func (c *macCapturer) Frames() <-chan *Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

// Stop ends capture and waits until the stream is released and Frames is
// closed, so Start can be called again straight away.
func (c *macCapturer) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (c *macCapturer) SetDisplay(index int) error {
	list, err := c.orderedDisplays()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(list) {
		return ErrDisplayNotFound
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.config.DisplayIndex = index
	if !c.running || c.cancel == nil {
		return nil
	}
	stream, err := c.backend.start(list[index], c.config)
	if err != nil {
		return err
	}
	if c.stream != nil {
		c.stream.stop()
	}
	c.stream, c.display, c.index = stream, list[index], index
	return nil
}

func (c *macCapturer) Close() {
	c.Stop()
}

// copyBGRAToRGBA converts a w x h BGRA picture whose rows are srcStride bytes
// apart -- CoreVideo pads rows -- into tightly packed, opaque RGBA.
func copyBGRAToRGBA(dst, src []byte, w, h, srcStride int) {
	for y := 0; y < h; y++ {
		s := src[y*srcStride : y*srcStride+w*4]
		d := dst[y*w*4 : (y+1)*w*4]
		for x := 0; x < w*4; x += 4 {
			d[x+0] = s[x+2]
			d[x+1] = s[x+1]
			d[x+2] = s[x+0]
			d[x+3] = 255
		}
	}
}
