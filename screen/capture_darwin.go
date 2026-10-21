// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package screen

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/barahn/remotekit/internal/macos"
	"github.com/barahn/remotekit/permission"
)

// NewCapturer creates a macOS screen capturer built on ScreenCaptureKit,
// which needs macOS 12.3 or later.
//
// Capture needs the Screen Recording permission; without it Start returns an
// error matching ErrPermissionDenied and permission.ErrNotGranted (see
// permission.NotGrantedError), without calling ScreenCaptureKit, so no system
// dialog appears. A grant takes effect after the process is relaunched (see
// permission.Check). If the permission is revoked during capture, Frames
// closes. Displays works without the permission.
//
// Frames carry the display's native pixels, so a Retina display yields twice
// as many pixels each way as its size in points. Frame.Bounds and
// Display.Bounds are in points, in global coordinates: the space macOS posts
// input events in, which is what the input injector is given.
func NewCapturer(config CaptureConfig) (Capturer, error) {
	if err := macos.Load(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureNotReady, err)
	}
	if objc.GetClass("SCStream") == 0 {
		return nil, fmt.Errorf("%w: ScreenCaptureKit needs macOS 12.3 or later", ErrCaptureNotReady)
	}
	return newMacCapturer(sckBackend{}, config), nil
}

const (
	scStreamOutputTypeScreen  = 0
	scStreamErrorUserDeclined = -3801
	scFrameStatusComplete     = 0
	pixelFormatBGRA           = 0x42475241 // 'BGRA'
	kCMTimeFlagsValid         = 1
	cvPixelBufferLockReadOnly = 1
	sckTimeout                = 10 * time.Second
)

type sckBackend struct{}

func (sckBackend) screenRecording() permission.Status {
	return permission.Check(permission.ScreenRecording)
}

func (sckBackend) displays() ([]macDisplay, error) {
	var ids [32]uint32
	var n uint32
	if rc := macos.CGGetActiveDisplayList(uint32(len(ids)), &ids[0], &n); rc != 0 {
		return nil, fmt.Errorf("screen: CGGetActiveDisplayList failed: %d", rc)
	}
	main := macos.CGMainDisplayID()
	out := make([]macDisplay, 0, n)
	for _, id := range ids[:n] {
		r := macos.CGDisplayBounds(id)
		d := macDisplay{
			id: id,
			bounds: image.Rect(int(r.Origin.X), int(r.Origin.Y),
				int(r.Origin.X+r.Size.Width), int(r.Origin.Y+r.Size.Height)),
			main: id == main,
		}
		d.pixelW, d.pixelH = d.bounds.Dx(), d.bounds.Dy()
		if mode := macos.CGDisplayCopyDisplayMode(id); mode != 0 {
			d.pixelW = int(macos.CGDisplayModeGetPixelWidth(mode))
			d.pixelH = int(macos.CGDisplayModeGetPixelHeight(mode))
			macos.CGDisplayModeRelease(mode)
		}
		out = append(out, d)
	}
	return out, nil
}

// sckError turns a ScreenCaptureKit NSError into the capturer's errors: a
// refusal for want of Screen Recording becomes the permission error.
func sckError(what string, nserr objc.ID) error {
	err := macos.ErrorFrom(nserr)
	var ns *macos.NSError
	if errors.As(err, &ns) && ns.Code == scStreamErrorUserDeclined {
		return screenRecordingError()
	}
	return fmt.Errorf("%w: %s: %v", ErrCaptureNotReady, what, err)
}

// waitBlock calls send with a completion block taking (result, error) and
// waits for the block to run. The block is released only once it has run.
func waitBlock(send func(block objc.Block)) (result, nserr objc.ID, ok bool) {
	type reply struct{ result, err objc.ID }
	ch := make(chan reply, 1)
	block := objc.NewBlock(func(_ objc.Block, result, err objc.ID) {
		if result != 0 {
			result.Send(macos.Sel("retain"))
		}
		if err != 0 {
			err.Send(macos.Sel("retain"))
		}
		ch <- reply{result, err}
	})
	send(block)
	select {
	case r := <-ch:
		block.Release()
		return r.result, r.err, true
	case <-time.After(sckTimeout):
		return 0, 0, false // the block may still run, so it is not released
	}
}

// waitErrBlock is waitBlock for a completion block taking only an error.
func waitErrBlock(send func(block objc.Block)) (nserr objc.ID, ok bool) {
	ch := make(chan objc.ID, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) {
		if err != 0 {
			err.Send(macos.Sel("retain"))
		}
		ch <- err
	})
	send(block)
	select {
	case err := <-ch:
		block.Release()
		return err, true
	case <-time.After(sckTimeout):
		return 0, false
	}
}

func release(id objc.ID) {
	if id != 0 {
		id.Send(macos.Sel("release"))
	}
}

func (sckBackend) start(d macDisplay, cfg CaptureConfig) (macStream, error) {
	if err := registerOutputClass(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureNotReady, err)
	}
	var s *sckStream
	var err error
	macos.WithAutoreleasePool(func() { s, err = startSCK(d, cfg) })
	return s, err
}

func startSCK(d macDisplay, cfg CaptureConfig) (*sckStream, error) {
	sel := macos.Sel
	content, nserr, ok := waitBlock(func(b objc.Block) {
		macos.Class("SCShareableContent").Send(sel("getShareableContentWithCompletionHandler:"), b)
	})
	if !ok {
		return nil, fmt.Errorf("%w: ScreenCaptureKit did not list the displays", ErrCaptureNotReady)
	}
	if nserr != 0 {
		defer release(nserr)
		return nil, sckError("listing displays", nserr)
	}
	defer release(content)

	var display objc.ID
	list := content.Send(sel("displays"))
	for i := 0; i < int(list.Send(sel("count"))); i++ {
		candidate := list.Send(sel("objectAtIndex:"), i)
		if objc.Send[uint32](candidate, sel("displayID")) == d.id {
			display = candidate
			break
		}
	}
	if display == 0 {
		return nil, ErrDisplayNotFound
	}

	s := &sckStream{}
	s.filter = macos.Class("SCContentFilter").Send(sel("alloc")).
		Send(sel("initWithDisplay:excludingWindows:"), display, macos.Class("NSArray").Send(sel("array")))
	s.config = macos.Class("SCStreamConfiguration").Send(sel("new"))
	s.config.Send(sel("setWidth:"), uintptr(d.pixelW))
	s.config.Send(sel("setHeight:"), uintptr(d.pixelH))
	s.config.Send(sel("setPixelFormat:"), uint32(pixelFormatBGRA))
	s.config.Send(sel("setShowsCursor:"), cfg.IncludeCursor)
	s.config.Send(sel("setQueueDepth:"), 3)
	fps := cfg.TargetFPS
	if fps <= 0 {
		fps = 30
	}
	macos.MsgSendCMTime(s.config, sel("setMinimumFrameInterval:"),
		macos.CMTime{Value: 1, Timescale: int32(fps), Flags: kCMTimeFlagsValid}) // #nosec G115 -- a frame rate

	s.output = objc.ID(outputClass).Send(sel("new"))
	outputs.Store(s.output, s)
	s.stream = macos.Class("SCStream").Send(sel("alloc")).
		Send(sel("initWithFilter:configuration:delegate:"), s.filter, s.config, s.output)
	s.queue = objc.ID(macos.DispatchQueueCreate(&queueLabel[0], 0))

	var addErr objc.ID
	if !objc.Send[bool](s.stream, sel("addStreamOutput:type:sampleHandlerQueue:error:"),
		s.output, scStreamOutputTypeScreen, s.queue, &addErr) {
		s.releaseAll()
		return nil, sckError("adding the stream output", addErr)
	}

	startErr, ok := waitErrBlock(func(b objc.Block) {
		s.stream.Send(sel("startCaptureWithCompletionHandler:"), b)
	})
	if !ok {
		s.stop()
		return nil, fmt.Errorf("%w: ScreenCaptureKit did not start", ErrCaptureNotReady)
	}
	if startErr != 0 {
		defer release(startErr)
		s.releaseAll()
		return nil, sckError("starting capture", startErr)
	}
	return s, nil
}

// queueLabel names the dispatch queue sample buffers arrive on. It is a
// package variable so the C string outlives every queue.
var queueLabel = []byte("com.barahn.remotekit.screen\x00")

var (
	outputClass     objc.Class
	outputClassOnce sync.Once
	outputClassErr  error
	// outputs maps each output object to its stream, for the callbacks.
	outputs sync.Map // objc.ID -> *sckStream
	// frameStatusKey is SCStreamFrameInfoStatus's value, kept for the life
	// of the process.
	frameStatusKey objc.ID
)

// registerOutputClass defines the Objective-C class that receives sample
// buffers and the stream's stop. An Objective-C class lives for the life of
// the process, so it is defined once.
func registerOutputClass() error {
	outputClassOnce.Do(func() {
		frameStatusKey = macos.NSString("SCStreamUpdateFrameStatus").Send(macos.Sel("retain"))
		var protocols []*objc.Protocol
		for _, name := range []string{"SCStreamOutput", "SCStreamDelegate"} {
			if p := objc.GetProtocol(name); p != nil {
				protocols = append(protocols, p)
			}
		}
		outputClass, outputClassErr = objc.RegisterClass("RemotekitScreenOutput", objc.GetClass("NSObject"), protocols, nil,
			[]objc.MethodDef{
				{Cmd: macos.Sel("stream:didOutputSampleBuffer:ofType:"), Fn: didOutputSampleBuffer},
				{Cmd: macos.Sel("stream:didStopWithError:"), Fn: didStopWithError},
			})
	})
	return outputClassErr
}

// didOutputSampleBuffer runs on the stream's dispatch queue for every sample.
// It keeps the newest complete picture; converting it waits for the capture
// loop to ask, at the frame rate the consumer wants.
func didOutputSampleBuffer(self objc.ID, _ objc.SEL, _ objc.ID, sample uintptr, typ int) {
	if typ != scStreamOutputTypeScreen || sample == 0 {
		return
	}
	v, ok := outputs.Load(self)
	if !ok {
		return
	}
	// Idle and blank samples carry no new picture.
	if atts := macos.CMSampleBufferGetSampleAttachmentsArray(sample, false); atts != 0 && int(atts.Send(macos.Sel("count"))) > 0 {
		status := atts.Send(macos.Sel("objectAtIndex:"), 0).Send(macos.Sel("objectForKey:"), frameStatusKey)
		if status != 0 && int(status.Send(macos.Sel("integerValue"))) != scFrameStatusComplete {
			return
		}
	}
	pb := macos.CMSampleBufferGetImageBuffer(sample)
	if pb == 0 {
		return
	}
	v.(*sckStream).keep(pb)
}

func didStopWithError(self objc.ID, _ objc.SEL, _ objc.ID, nserr objc.ID) {
	if v, ok := outputs.Load(self); ok {
		v.(*sckStream).fail(sckError("capture stopped", nserr))
	}
}

// sckStream is one SCStream and the newest picture it delivered.
type sckStream struct {
	stream, filter, config, output, queue objc.ID

	mu     sync.Mutex
	pb     uintptr // the newest CVPixelBuffer, retained
	err    error
	closed bool

	// The last converted picture, so an unchanged screen is copied rather
	// than converted again. Only latest touches these.
	lastPB  uintptr
	lastImg *image.RGBA
}

func (s *sckStream) keep(pb uintptr) {
	macos.CFRetain(pb)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		macos.CFRelease(pb)
		return
	}
	old := s.pb
	s.pb = pb
	s.mu.Unlock()
	if old != 0 {
		macos.CFRelease(old)
	}
}

func (s *sckStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *sckStream) latest() (*image.RGBA, error) {
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return nil, err
	}
	pb := s.pb
	if pb == 0 {
		s.mu.Unlock()
		return nil, nil
	}
	macos.CFRetain(pb)
	s.mu.Unlock()
	defer macos.CFRelease(pb)

	if pb == s.lastPB && s.lastImg != nil {
		img := image.NewRGBA(s.lastImg.Rect)
		copy(img.Pix, s.lastImg.Pix)
		return img, nil
	}
	if macos.CVPixelBufferGetPixelFormatType(pb) != pixelFormatBGRA {
		return nil, nil
	}
	if macos.CVPixelBufferLockBaseAddress(pb, cvPixelBufferLockReadOnly) != 0 {
		return nil, nil
	}
	defer macos.CVPixelBufferUnlockBaseAddress(pb, cvPixelBufferLockReadOnly)
	w := int(macos.CVPixelBufferGetWidth(pb))
	h := int(macos.CVPixelBufferGetHeight(pb))
	stride := int(macos.CVPixelBufferGetBytesPerRow(pb))
	base := macos.CVPixelBufferGetBaseAddress(pb)
	if base == nil || w <= 0 || h <= 0 || stride < w*4 {
		return nil, nil
	}
	src := unsafe.Slice((*byte)(base), stride*h) // #nosec G103 -- CoreVideo's locked pixel buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copyBGRAToRGBA(img.Pix, src, w, h, stride)

	if s.lastPB != 0 {
		macos.CFRelease(s.lastPB)
	}
	macos.CFRetain(pb)
	s.lastPB, s.lastImg = pb, img
	return img, nil
}

func (s *sckStream) stop() {
	macos.WithAutoreleasePool(func() {
		waitErrBlock(func(b objc.Block) {
			s.stream.Send(macos.Sel("stopCaptureWithCompletionHandler:"), b)
		})
		s.releaseAll()
	})
}

// releaseAll lets go of everything the stream holds. No sample is kept after
// it, even one already on its way.
func (s *sckStream) releaseAll() {
	s.mu.Lock()
	s.closed = true
	pb := s.pb
	s.pb = 0
	s.mu.Unlock()
	if pb != 0 {
		macos.CFRelease(pb)
	}
	if s.lastPB != 0 {
		macos.CFRelease(s.lastPB)
		s.lastPB, s.lastImg = 0, nil
	}
	outputs.Delete(s.output)
	for _, id := range []objc.ID{s.stream, s.output, s.config, s.filter, s.queue} {
		release(id)
	}
}
