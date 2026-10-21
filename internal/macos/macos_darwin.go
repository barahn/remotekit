// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package macos

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// CGPoint, CGSize and CGRect mirror CoreGraphics' structs of the same names.
type CGPoint struct{ X, Y float64 }

type CGSize struct{ Width, Height float64 }

type CGRect struct {
	Origin CGPoint
	Size   CGSize
}

// CMTime mirrors CoreMedia's CMTime.
type CMTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

// The framework functions remotekit calls. They are nil until Load succeeds.
var (
	CGPreflightScreenCaptureAccess func() bool
	CGRequestScreenCaptureAccess   func() bool
	AXIsProcessTrusted             func() bool
	AXIsProcessTrustedWithOptions  func(options objc.ID) bool

	CGGetActiveDisplayList      func(maxDisplays uint32, displays *uint32, count *uint32) int32
	CGMainDisplayID             func() uint32
	CGDisplayBounds             func(display uint32) CGRect
	CGDisplayCopyDisplayMode    func(display uint32) uintptr
	CGDisplayModeGetPixelWidth  func(mode uintptr) uintptr
	CGDisplayModeGetPixelHeight func(mode uintptr) uintptr
	CGDisplayModeRelease        func(mode uintptr)

	CGEventCreate                   func(source uintptr) uintptr
	CGEventGetLocation              func(event uintptr) CGPoint
	CGEventCreateMouseEvent         func(source uintptr, typ uint32, at CGPoint, button uint32) uintptr
	CGEventCreateKeyboardEvent      func(source uintptr, key uint16, down bool) uintptr
	CGEventCreateScrollWheelEvent2  func(source uintptr, units uint32, wheelCount uint32, wheel1, wheel2, wheel3 int32) uintptr
	CGEventKeyboardSetUnicodeString func(event uintptr, length uint64, text *uint16)
	CGEventSetFlags                 func(event uintptr, flags uint64)
	CGEventSetType                  func(event uintptr, typ uint32)
	CGEventSetIntegerValueField     func(event uintptr, field uint32, value int64)
	CGEventPost                     func(tap uint32, event uintptr)

	CFRetain  func(cf uintptr) uintptr
	CFRelease func(cf uintptr)

	CMSampleBufferGetImageBuffer            func(sample uintptr) uintptr
	CMSampleBufferGetSampleAttachmentsArray func(sample uintptr, create bool) objc.ID

	CVPixelBufferLockBaseAddress    func(buf uintptr, flags uint64) int32
	CVPixelBufferUnlockBaseAddress  func(buf uintptr, flags uint64) int32
	CVPixelBufferGetBaseAddress     func(buf uintptr) unsafe.Pointer
	CVPixelBufferGetBytesPerRow     func(buf uintptr) uintptr
	CVPixelBufferGetWidth           func(buf uintptr) uintptr
	CVPixelBufferGetHeight          func(buf uintptr) uintptr
	CVPixelBufferGetPixelFormatType func(buf uintptr) uint32

	DispatchQueueCreate func(label *byte, attr uintptr) uintptr
)

// MsgSendCMTime sends a message whose one argument is a CMTime, which the
// variadic objc.Send cannot pass by value.
var MsgSendCMTime func(id objc.ID, sel objc.SEL, t CMTime)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens the frameworks and binds the functions above. It is safe to call
// from any goroutine, any number of times; only the first call does work.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	open := func(path string) (uintptr, error) {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return 0, fmt.Errorf("macos: open %s: %w", path, err)
		}
		return h, nil
	}
	const fw = "/System/Library/Frameworks/"
	libs := map[string]uintptr{}
	for _, name := range []string{"CoreGraphics", "CoreFoundation", "CoreMedia", "CoreVideo", "ApplicationServices", "Foundation", "ScreenCaptureKit"} {
		h, err := open(fw + name + ".framework/" + name)
		if err != nil {
			return err
		}
		libs[name] = h
	}
	system, err := open("/usr/lib/libSystem.B.dylib")
	if err != nil {
		return err
	}
	libobjc, err := open("/usr/lib/libobjc.A.dylib")
	if err != nil {
		return err
	}

	bind := []struct {
		fn   any
		lib  uintptr
		name string
	}{
		{&CGPreflightScreenCaptureAccess, libs["CoreGraphics"], "CGPreflightScreenCaptureAccess"},
		{&CGRequestScreenCaptureAccess, libs["CoreGraphics"], "CGRequestScreenCaptureAccess"},
		{&AXIsProcessTrusted, libs["ApplicationServices"], "AXIsProcessTrusted"},
		{&AXIsProcessTrustedWithOptions, libs["ApplicationServices"], "AXIsProcessTrustedWithOptions"},
		{&CGGetActiveDisplayList, libs["CoreGraphics"], "CGGetActiveDisplayList"},
		{&CGMainDisplayID, libs["CoreGraphics"], "CGMainDisplayID"},
		{&CGDisplayBounds, libs["CoreGraphics"], "CGDisplayBounds"},
		{&CGDisplayCopyDisplayMode, libs["CoreGraphics"], "CGDisplayCopyDisplayMode"},
		{&CGDisplayModeGetPixelWidth, libs["CoreGraphics"], "CGDisplayModeGetPixelWidth"},
		{&CGDisplayModeGetPixelHeight, libs["CoreGraphics"], "CGDisplayModeGetPixelHeight"},
		{&CGDisplayModeRelease, libs["CoreGraphics"], "CGDisplayModeRelease"},
		{&CGEventCreate, libs["CoreGraphics"], "CGEventCreate"},
		{&CGEventGetLocation, libs["CoreGraphics"], "CGEventGetLocation"},
		{&CGEventCreateMouseEvent, libs["CoreGraphics"], "CGEventCreateMouseEvent"},
		{&CGEventCreateKeyboardEvent, libs["CoreGraphics"], "CGEventCreateKeyboardEvent"},
		{&CGEventCreateScrollWheelEvent2, libs["CoreGraphics"], "CGEventCreateScrollWheelEvent2"},
		{&CGEventKeyboardSetUnicodeString, libs["CoreGraphics"], "CGEventKeyboardSetUnicodeString"},
		{&CGEventSetFlags, libs["CoreGraphics"], "CGEventSetFlags"},
		{&CGEventSetType, libs["CoreGraphics"], "CGEventSetType"},
		{&CGEventSetIntegerValueField, libs["CoreGraphics"], "CGEventSetIntegerValueField"},
		{&CGEventPost, libs["CoreGraphics"], "CGEventPost"},
		{&CFRetain, libs["CoreFoundation"], "CFRetain"},
		{&CFRelease, libs["CoreFoundation"], "CFRelease"},
		{&CMSampleBufferGetImageBuffer, libs["CoreMedia"], "CMSampleBufferGetImageBuffer"},
		{&CMSampleBufferGetSampleAttachmentsArray, libs["CoreMedia"], "CMSampleBufferGetSampleAttachmentsArray"},
		{&CVPixelBufferLockBaseAddress, libs["CoreVideo"], "CVPixelBufferLockBaseAddress"},
		{&CVPixelBufferUnlockBaseAddress, libs["CoreVideo"], "CVPixelBufferUnlockBaseAddress"},
		{&CVPixelBufferGetBaseAddress, libs["CoreVideo"], "CVPixelBufferGetBaseAddress"},
		{&CVPixelBufferGetBytesPerRow, libs["CoreVideo"], "CVPixelBufferGetBytesPerRow"},
		{&CVPixelBufferGetWidth, libs["CoreVideo"], "CVPixelBufferGetWidth"},
		{&CVPixelBufferGetHeight, libs["CoreVideo"], "CVPixelBufferGetHeight"},
		{&CVPixelBufferGetPixelFormatType, libs["CoreVideo"], "CVPixelBufferGetPixelFormatType"},
		{&DispatchQueueCreate, system, "dispatch_queue_create"},
	}
	for _, b := range bind {
		if _, err := purego.Dlsym(b.lib, b.name); err != nil {
			return fmt.Errorf("macos: %s is missing: %w", b.name, err)
		}
		purego.RegisterLibFunc(b.fn, b.lib, b.name)
	}
	msgSend, err := purego.Dlsym(libobjc, "objc_msgSend")
	if err != nil {
		return fmt.Errorf("macos: objc_msgSend is missing: %w", err)
	}
	purego.RegisterFunc(&MsgSendCMTime, msgSend)
	return nil
}

var (
	selMu    sync.Mutex
	selCache = map[string]objc.SEL{}
)

// Sel returns the selector named name, registering it once.
func Sel(name string) objc.SEL {
	selMu.Lock()
	defer selMu.Unlock()
	s, ok := selCache[name]
	if !ok {
		s = objc.RegisterName(name)
		selCache[name] = s
	}
	return s
}

// Class returns the Objective-C class named name, as an ID to send to.
func Class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// NSString returns an autoreleased NSString holding s.
func NSString(s string) objc.ID {
	return Class("NSString").Send(Sel("stringWithUTF8String:"), s)
}

// GoString returns the contents of an NSString.
func GoString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	return objc.Send[string](s, Sel("UTF8String"))
}

// NSError describes an NSError as its domain, code and description.
type NSError struct {
	Domain      string
	Code        int
	Description string
}

func (e *NSError) Error() string {
	return fmt.Sprintf("%s (%s %d)", e.Description, e.Domain, e.Code)
}

// ErrorFrom copies an NSError into a Go error; nil when err is nil.
func ErrorFrom(err objc.ID) error {
	if err == 0 {
		return nil
	}
	return &NSError{
		Domain:      GoString(err.Send(Sel("domain"))),
		Code:        int(err.Send(Sel("code"))),
		Description: GoString(err.Send(Sel("localizedDescription"))),
	}
}

// WithAutoreleasePool runs fn inside an autorelease pool, so the autoreleased
// objects it makes are released when it returns rather than leaking on a
// thread that has no pool of its own.
//
// The pool belongs to the thread it was made on, so the goroutine is held to
// its thread until fn returns.
func WithAutoreleasePool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := Class("NSAutoreleasePool").Send(Sel("new"))
	defer pool.Send(Sel("drain"))
	fn()
}
