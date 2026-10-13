// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/input"
	"github.com/barahn/remotekit/screen"
)

// fakeCapturer has two monitors of different sizes, side by side, and streams
// a flat frame of whichever is selected.
type fakeCapturer struct {
	mu      sync.Mutex
	index   int
	frames  chan *screen.Frame
	stopped atomic.Bool
}

var fakeDisplays = []screen.Display{
	{Index: 0, Name: "LEFT", Bounds: image.Rect(0, 0, 160, 96), Primary: true},
	{Index: 1, Name: "RIGHT", Bounds: image.Rect(160, 0, 160+128, 80)},
}

func (c *fakeCapturer) Displays() ([]screen.Display, error) { return fakeDisplays, nil }

func (c *fakeCapturer) Start(ctx context.Context) error {
	c.frames = make(chan *screen.Frame, 2)
	go func() {
		defer close(c.frames)
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		for n := uint64(1); ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if c.stopped.Load() {
				return
			}
			c.mu.Lock()
			d := fakeDisplays[c.index]
			c.mu.Unlock()
			img := image.NewRGBA(image.Rect(0, 0, d.Bounds.Dx(), d.Bounds.Dy()))
			shade := uint8(n % 200)
			for i := 0; i < len(img.Pix); i += 4 {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = shade, 80, 160, 255
			}
			select {
			case c.frames <- &screen.Frame{Image: img, Bounds: d.Bounds, DisplayIndex: d.Index, CapturedAt: time.Now(), SequenceNum: n}:
			default:
			}
		}
	}()
	return nil
}

func (c *fakeCapturer) Frames() <-chan *screen.Frame { return c.frames }
func (c *fakeCapturer) Stop()                        { c.stopped.Store(true) }

func (c *fakeCapturer) SetDisplay(index int) error {
	if index < 0 || index >= len(fakeDisplays) {
		return screen.ErrDisplayNotFound
	}
	c.mu.Lock()
	c.index = index
	c.mu.Unlock()
	return nil
}

// boundsInjector records the bounds input is aimed at.
type boundsInjector struct {
	input.Injector
	mu     sync.Mutex
	bounds image.Rectangle
}

func (i *boundsInjector) SetScreenBounds(b image.Rectangle) {
	i.mu.Lock()
	i.bounds = b
	i.mu.Unlock()
}

func (i *boundsInjector) get() image.Rectangle {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.bounds
}

func TestViewerChoosesTheMonitor(t *testing.T) {
	inj := &boundsInjector{}
	r := New(Config{ID: "agent-1"})
	r.injector = inj
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})

	lists := make(chan bark.DisplaysPayload, 16)
	v.OnDisplays(func(p bark.DisplaysPayload) { lists <- p })
	frames, notices := session(t, r, v)

	// waitFor returns the first list that satisfies ok.
	waitFor := func(what string, ok func(bark.DisplaysPayload) bool) bark.DisplaysPayload {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case p := <-lists:
				if ok(p) {
					return p
				}
			case n := <-notices:
				t.Fatalf("notice while waiting for %s: %+v", what, n)
			case <-deadline:
				t.Fatalf("no displays message with %s within 20s", what)
			}
		}
	}

	first := waitFor("the first monitor active", func(p bark.DisplaysPayload) bool { return p.Active == 0 })
	if len(first.Displays) != 2 || first.Displays[1].Name != "RIGHT" || first.Displays[1].X != 160 || first.Displays[1].Width != 128 {
		t.Fatalf("displays = %+v, want both monitors with their geometry", first.Displays)
	}
	if got := firstFrame(t, frames, notices); got.Dx() != 160 {
		t.Fatalf("first frame is %v wide, want the left monitor's 160", got)
	}

	if err := v.SelectDisplay(1); err != nil {
		t.Fatal(err)
	}
	waitFor("the second monitor active", func(p bark.DisplaysPayload) bool { return p.Active == 1 })

	// Frames now come from the right monitor, and input is aimed at it.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case rect := <-frames:
			if rect.Dx() != 128 {
				continue
			}
			if b := inj.get(); b != fakeDisplays[1].Bounds {
				t.Fatalf("input bounds = %v, want the right monitor's %v", b, fakeDisplays[1].Bounds)
			}
			goto selected
		case <-deadline:
			t.Fatal("no frame of the right monitor decoded within 10s")
		}
	}
selected:

	if err := v.SelectDisplay(7); err != nil {
		t.Fatal(err)
	}
	if p := waitFor("an error", func(p bark.DisplaysPayload) bool { return p.Error != "" }); p.Active != 1 {
		t.Fatalf("after a bad index, Active = %d, want it unchanged at 1", p.Active)
	}
}

func TestMonitorListWithheldWithoutScreenConsent(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	r.RequireScreenViewConsent(true)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	lists := make(chan bark.DisplaysPayload, 4)
	v.OnDisplays(func(p bark.DisplaysPayload) { lists <- p })
	_, notices := session(t, r, v)

	select {
	case n := <-notices:
		if n.Type != "consent_required" {
			t.Fatalf("notice = %+v, want consent_required", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no consent_required notice")
	}
	select {
	case p := <-lists:
		t.Fatalf("got the monitor list without screen_view consent: %+v", p)
	case <-time.After(time.Second):
	}
}
