// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

// X11 screen capture implementation using pure Go xgb/shm for
// high-performance frame acquisition with zero CGo overhead per frame.
//
// Falls back to xproto.GetImage when the MIT-SHM extension is unavailable.
//
// No build dependencies required — this is 100% pure Go using the xgb
// library for X11 protocol communication.

package screen

import (
	"context"
	"fmt"
	"image"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/barahn/remotekit/internal/x11"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xproto"
)

// System call numbers for SysV shared memory IPC.
const (
	sysIPCPrivate = 0
	sysIPCCreate  = 00001000 // IPC_CREAT
	sysIPCRemove  = 0        // IPC_RMID
)

// x11Capturer implements Capturer using pure Go X11 with XShm acceleration.
type x11Capturer struct {
	config CaptureConfig

	mu   sync.Mutex
	conn *xgb.Conn
	root xproto.Window

	width  uint16
	height uint16
	depth  byte

	// randrReady records whether the RANDR extension initialized. Without it
	// the X screen can only be reported as one display, which on a
	// multi-monitor setup is every monitor spanned into a single image.
	randrReady bool

	// SHM state
	shmAvailable bool
	shmSeg       shm.Seg
	shmBuf       []byte  // pre-created slice over the SysV SHM segment
	shmAddr      uintptr // raw address for SHMDT cleanup
	shmSize      int

	frames   chan *Frame
	running  atomic.Bool
	seqNum   atomic.Uint64
	stopOnce sync.Once
	cancel   context.CancelFunc
}

// NewCapturer creates a new platform-specific Capturer.
// On Linux, this auto-detects Wayland vs X11 and returns the appropriate capturer.
func NewCapturer(config CaptureConfig) (Capturer, error) {
	if config.FrameBufferSize <= 0 {
		config.FrameBufferSize = 2
	}
	if config.TargetFPS <= 0 {
		config.TargetFPS = 30
	}
	if config.DisplayIndex < 0 {
		config.DisplayIndex = 0
	}

	ds := DetectDisplayServer()
	// Under Wayland, prioritize native Wayland / PipeWire / Mutter ScreenCast capturer
	if ds == DisplayServerWayland {
		if cap, err := newWaylandCapturer(config); err == nil {
			return cap, nil
		}
	}

	// When running under X11 or with DISPLAY available, prioritize direct X11 capture
	if ds == DisplayServerX11 || os.Getenv("DISPLAY") != "" {
		if cap, err := newX11Capturer(config); err == nil {
			return cap, nil
		}
	}

	// Default fallback: try Wayland first if not tried, then X11
	if ds != DisplayServerWayland {
		if wcap, werr := newWaylandCapturer(config); werr == nil {
			return wcap, nil
		}
	}

	cap, err := newX11Capturer(config)
	if err == nil {
		return cap, nil
	}

	return nil, err
}

func newX11Capturer(config CaptureConfig) (*x11Capturer, error) {
	conn, err := x11.NewConn()
	if err != nil {
		return nil, fmt.Errorf("%w: cannot connect to X11 display (is $DISPLAY set?): %v", ErrCaptureNotReady, err)
	}

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	root := screen.Root
	width := screen.WidthInPixels
	height := screen.HeightInPixels
	depth := screen.RootDepth

	c := &x11Capturer{
		config: config,
		conn:   conn,
		root:   root,
		width:  width,
		height: height,
		depth:  depth,
	}

	// RANDR is what makes the individual monitors visible. Without it the
	// capturer still works, but only as a single spanned screen.
	if rErr := x11.Enable(conn, x11.RANDR); rErr == nil {
		c.randrReady = true
	}

	// Try to initialize MIT-SHM extension
	if err := c.initShm(); err != nil {
		// SHM not available — will fall back to GetImage (slower)
		c.shmAvailable = false
	}

	return c, nil
}

// initShm sets up the MIT-SHM shared memory segment for zero-copy capture.
func (c *x11Capturer) initShm() error {
	if err := x11.Enable(c.conn, x11.SHM); err != nil {
		return fmt.Errorf("MIT-SHM extension not available: %w", err)
	}

	imgSize := int(c.width) * int(c.height) * 4 // 4 bytes per pixel (BGRx)

	// Create SysV shared memory segment
	shmID, _, errno := syscall.Syscall(
		syscall.SYS_SHMGET,
		uintptr(sysIPCPrivate),
		uintptr(imgSize),
		uintptr(0600|sysIPCCreate),
	)
	if errno != 0 {
		return fmt.Errorf("shmget failed: %v", errno)
	}

	// Attach shared memory to process address space
	addr, _, errno := syscall.Syscall(syscall.SYS_SHMAT, shmID, 0, 0)
	if errno != 0 {
		return fmt.Errorf("shmat failed: %v", errno)
	}

	// Create a byte slice view of the shared memory segment.
	shmBuf := unsafe.Slice((*byte)(uintptrToPointer(addr)), imgSize) // #nosec G103 -- required for X11 SHM slice mapping

	// Helper to detach SHM on setup failure
	cleanupShm := func() {
		_, _, _ = syscall.Syscall(syscall.SYS_SHMDT, addr, 0, 0)
	}

	// Allocate X11 SHM segment ID
	seg, err := shm.NewSegId(c.conn)
	if err != nil {
		cleanupShm()
		return fmt.Errorf("failed to allocate SHM segment ID: %w", err)
	}

	// Attach SHM segment to X server
	shmAttachCookie := shm.AttachChecked(c.conn, seg, uint32(shmID), false) // #nosec G115 -- shmID fits in uint32
	if err := shmAttachCookie.Check(); err != nil {
		cleanupShm()
		return fmt.Errorf("failed to attach SHM to X server: %w", err)
	}

	// Mark segment for auto-removal when last process detaches
	_, _, _ = syscall.Syscall(syscall.SYS_SHMCTL, shmID, 0 /* IPC_RMID */, 0)

	c.shmSeg = seg
	c.shmBuf = shmBuf
	c.shmAddr = addr
	c.shmSize = imgSize
	c.shmAvailable = true

	return nil
}

// Displays enumerates the monitors attached to the X screen.
//
// Enumeration is not cached: monitors are hot-plugged, rearranged and turned
// off while a session is live, and a stale list would offer the operator a
// monitor that no longer exists.
func (c *x11Capturer) Displays() ([]Display, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, ErrCaptureNotReady
	}

	return c.displaysLocked(), nil
}

// displaysLocked enumerates monitors via RANDR, falling back to the whole X
// screen as a single display. Callers must hold c.mu.
func (c *x11Capturer) displaysLocked() []Display {
	whole := []Display{{
		Index:   0,
		Name:    "X11 Screen (default)",
		Bounds:  image.Rect(0, 0, int(c.width), int(c.height)),
		Primary: true,
	}}

	if !c.randrReady {
		return whole
	}

	reply, err := randr.GetMonitors(c.conn, c.root, true).Reply()
	if err != nil || reply == nil || len(reply.Monitors) == 0 {
		return whole
	}

	displays := make([]Display, 0, len(reply.Monitors))
	for _, m := range reply.Monitors {
		if m.Width == 0 || m.Height == 0 {
			continue
		}
		name := fmt.Sprintf("Monitor %d", len(displays)+1)
		if atomName, nErr := xproto.GetAtomName(c.conn, m.Name).Reply(); nErr == nil && atomName != nil && atomName.Name != "" {
			name = atomName.Name
		}
		displays = append(displays, Display{
			Index: len(displays),
			Name:  name,
			Bounds: image.Rect(
				int(m.X), int(m.Y),
				int(m.X)+int(m.Width), int(m.Y)+int(m.Height),
			),
			Primary: m.Primary,
		})
	}

	if len(displays) == 0 {
		return whole
	}
	return displays
}

// activeRectLocked returns the region of the X screen to grab: the selected
// monitor's rectangle in global root coordinates. An index that no longer
// resolves (the monitor was unplugged mid-session) falls back to the primary
// monitor rather than to the full spanned screen, because silently widening
// the capture would leak the other monitors' contents to the viewer.
//
// Callers must hold c.mu.
func (c *x11Capturer) activeRectLocked() image.Rectangle {
	displays := c.displaysLocked()

	idx := c.config.DisplayIndex
	if idx >= 0 && idx < len(displays) {
		return displays[idx].Bounds
	}
	for _, d := range displays {
		if d.Primary {
			return d.Bounds
		}
	}
	return displays[0].Bounds
}

// Start begins capturing frames at the configured rate.
func (c *x11Capturer) Start(ctx context.Context) error {
	if c.running.Load() {
		return ErrAlreadyStarted
	}

	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return ErrCaptureNotReady
	}
	c.mu.Unlock()

	c.frames = make(chan *Frame, c.config.FrameBufferSize)
	c.running.Store(true)
	c.seqNum.Store(0)
	c.stopOnce = sync.Once{}

	captureCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	go c.captureLoop(captureCtx)

	return nil
}

// Frames returns the channel delivering captured frames.
func (c *x11Capturer) Frames() <-chan *Frame {
	return c.frames
}

// Stop halts capture and closes the Frames channel.
func (c *x11Capturer) Stop() {
	c.stopOnce.Do(func() {
		c.running.Store(false)
		if c.cancel != nil {
			c.cancel()
		}
	})
}

// SetDisplay changes which monitor to capture. It takes effect on the next
// frame: the capture loop re-reads the active rectangle every tick.
func (c *x11Capturer) SetDisplay(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrCaptureNotReady
	}

	if index < 0 || index >= len(c.displaysLocked()) {
		return ErrDisplayNotFound
	}

	c.config.DisplayIndex = index
	return nil
}

// captureLoop is the main frame acquisition goroutine.
func (c *x11Capturer) captureLoop(ctx context.Context) {
	defer close(c.frames)

	interval := time.Second / time.Duration(c.config.TargetFPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Sized to the full screen so a switch to a larger monitor, or a
	// resolution change, never has to reallocate.
	rgbaBuf := make([]byte, int(c.width)*int(c.height)*4)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.running.Load() {
				return
			}

			// Re-read every tick so SetDisplay, a resolution change and a
			// hot-plugged monitor all take effect on the next frame without
			// restarting capture.
			c.mu.Lock()
			rect := c.activeRectLocked()
			dispIdx := c.config.DisplayIndex
			c.mu.Unlock()

			w := rect.Dx()
			h := rect.Dy()
			if w <= 0 || h <= 0 {
				continue
			}
			if need := w * h * 4; need > len(rgbaBuf) {
				rgbaBuf = make([]byte, need)
			}

			var err error
			if c.shmAvailable {
				err = c.captureSHM(rgbaBuf, rect)
			} else {
				err = c.captureFallback(rgbaBuf, rect)
			}

			if err != nil {
				continue // Skip frame on error
			}

			// Create image from captured buffer (copy pixels to decouple from reusable buffer)
			rgba := image.NewRGBA(image.Rect(0, 0, w, h))
			copy(rgba.Pix, rgbaBuf[:w*h*4])

			frame := &Frame{
				Image: rgba,
				// The monitor's rectangle in global screen coordinates, not
				// an origin-anchored one: input injection needs the offset to
				// aim clicks at the monitor actually being viewed.
				Bounds:       rect,
				DisplayIndex: dispIdx,
				CapturedAt:   time.Now(),
				SequenceNum:  c.seqNum.Add(1),
			}

			// Non-blocking send: drop frame if consumer is slow
			select {
			case c.frames <- frame:
			default:
				// Frame dropped — consumer too slow
			}
		}
	}
}

// x11Rect narrows a capture rectangle to the types the X11 protocol uses:
// int16 for coordinates and uint16 for dimensions.
//
// A monitor laid out beyond 32767px on either axis cannot be addressed by the
// protocol at all, so the conversion would wrap and silently grab a region
// somewhere else entirely. That is far-fetched today and cheap to rule out,
// and an error naming the rectangle beats a frame of the wrong part of the
// screen. Callers skip the frame.
func x11Rect(rect image.Rectangle) (x, y int16, w, h uint16, err error) {
	const maxCoord = 32767 // math.MaxInt16
	const maxDim = 65535   // math.MaxUint16

	if rect.Min.X < -maxCoord-1 || rect.Min.X > maxCoord ||
		rect.Min.Y < -maxCoord-1 || rect.Min.Y > maxCoord {
		return 0, 0, 0, 0, fmt.Errorf("screen: capture origin (%d,%d) is outside the X11 coordinate range", rect.Min.X, rect.Min.Y)
	}
	if rect.Dx() < 0 || rect.Dx() > maxDim || rect.Dy() < 0 || rect.Dy() > maxDim {
		return 0, 0, 0, 0, fmt.Errorf("screen: capture size %dx%d is outside the X11 dimension range", rect.Dx(), rect.Dy())
	}

	// #nosec G115 -- every value is range-checked immediately above, which is
	// the whole purpose of this function; gosec does not follow the guards.
	return int16(rect.Min.X), int16(rect.Min.Y), uint16(rect.Dx()), uint16(rect.Dy()), nil
}

// captureSHM captures a frame using XShm (zero-copy from X server to shared memory).
//
// rect is the region of the root window to grab, in global screen coordinates.
// Asking the X server for just that region is what keeps a multi-monitor host
// from encoding and shipping every monitor when the operator is looking at one.
func (c *x11Capturer) captureSHM(dst []byte, rect image.Rectangle) error {
	x, y, w, h, err := x11Rect(rect)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cookie := shm.GetImage(
		c.conn,
		xproto.Drawable(c.root),
		x, y,
		w, h,
		0xFFFFFFFF, // AllPlanes
		byte(xproto.ImageFormatZPixmap),
		c.shmSeg,
		0,
	)

	if _, err := cookie.Reply(); err != nil {
		return fmt.Errorf("XShmGetImage failed: %w", err)
	}

	// Use pre-created slice view of the shared memory frame buffer
	bgraToRGBA(c.shmBuf, dst, rect.Dx(), rect.Dy())

	return nil
}

// captureFallback captures a frame using xproto.GetImage (slower, no SHM).
func (c *x11Capturer) captureFallback(dst []byte, rect image.Rectangle) error {
	x, y, w, h, err := x11Rect(rect)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cookie := xproto.GetImage(
		c.conn,
		xproto.ImageFormatZPixmap,
		xproto.Drawable(c.root),
		x, y,
		w, h,
		0xFFFFFFFF,
	)

	reply, rErr := cookie.Reply()
	if rErr != nil {
		return fmt.Errorf("XGetImage failed: %w", rErr)
	}

	bgraToRGBA(reply.Data, dst, rect.Dx(), rect.Dy())
	return nil
}

// bgraToRGBA converts BGRA pixel data to RGBA.
// X11 uses BGRA byte order on little-endian systems.
func bgraToRGBA(src, dst []byte, w, h int) {
	total := w * h
	// The X server can return a short reply (a monitor unplugged between the
	// request and the reply), and the SHM segment is sized for the full
	// screen while a grab covers one monitor. Clamp rather than panic.
	if max := len(src) / 4; total > max {
		total = max
	}
	if max := len(dst) / 4; total > max {
		total = max
	}
	for i := 0; i < total; i++ {
		si := i * 4
		dst[si+0] = src[si+2] // R <- B
		dst[si+1] = src[si+1] // G <- G
		dst[si+2] = src[si+0] // B <- R
		dst[si+3] = 255       // A = opaque
	}
}

// Close releases all X11 resources including the SHM segment.
func (c *x11Capturer) Close() {
	c.Stop()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.shmAvailable {
		shm.Detach(c.conn, c.shmSeg)
		_, _, _ = syscall.Syscall(syscall.SYS_SHMDT, c.shmAddr, 0, 0)
		c.shmAvailable = false
	}

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// uintptrToPointer converts a uintptr address to unsafe.Pointer.
func uintptrToPointer(ptr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&ptr)) // #nosec G103 -- required for X11 SHM pointer conversion
}
