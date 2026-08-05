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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jezek/xgb"
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
// On Linux, this returns an X11-based capturer with XShm acceleration.
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

	conn, err := xgb.NewConn()
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

	// Try to initialize MIT-SHM extension
	if err := c.initShm(); err != nil {
		// SHM not available — will fall back to GetImage (slower)
		c.shmAvailable = false
	}

	return c, nil
}

// initShm sets up the MIT-SHM shared memory segment for zero-copy capture.
func (c *x11Capturer) initShm() error {
	if err := shm.Init(c.conn); err != nil {
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
	shmBuf := unsafe.Slice((*byte)(uintptrToPointer(addr)), imgSize)

	// Mark segment for auto-removal when last process detaches
	_, _, _ = syscall.Syscall(syscall.SYS_SHMCTL, shmID, 0 /* IPC_RMID */, 0)

	// Helper to detach SHM on setup failure
	cleanupShm := func() {
		syscall.Syscall(syscall.SYS_SHMDT, addr, 0, 0)
	}

	// Allocate X11 SHM segment ID
	seg, err := shm.NewSegId(c.conn)
	if err != nil {
		cleanupShm()
		return fmt.Errorf("failed to allocate SHM segment ID: %w", err)
	}

	// Attach SHM segment to X server
	shmAttachCookie := shm.AttachChecked(c.conn, seg, uint32(shmID), false)
	if err := shmAttachCookie.Check(); err != nil {
		cleanupShm()
		return fmt.Errorf("failed to attach SHM to X server: %w", err)
	}

	c.shmSeg = seg
	c.shmBuf = shmBuf
	c.shmAddr = addr
	c.shmSize = imgSize
	c.shmAvailable = true

	return nil
}

// Displays enumerates available X11 screens.
func (c *x11Capturer) Displays() ([]Display, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, ErrCaptureNotReady
	}

	d := Display{
		Index: 0,
		Name:  fmt.Sprintf("X11 Screen (default)"),
		Bounds: image.Rect(
			0, 0,
			int(c.width),
			int(c.height),
		),
		Primary: true,
	}

	return []Display{d}, nil
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

// SetDisplay changes which display to capture.
func (c *x11Capturer) SetDisplay(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrCaptureNotReady
	}

	if index != 0 {
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

	w := int(c.width)
	h := int(c.height)

	// Pre-allocate a reusable buffer for BGRA→RGBA conversion
	rgbaBuf := make([]byte, w*h*4)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.running.Load() {
				return
			}

			var err error
			if c.shmAvailable {
				err = c.captureSHM(rgbaBuf, w, h)
			} else {
				err = c.captureFallback(rgbaBuf, w, h)
			}

			if err != nil {
				continue // Skip frame on error
			}

			// Create image from captured buffer (copy pixels to decouple from reusable buffer)
			rgba := image.NewRGBA(image.Rect(0, 0, w, h))
			copy(rgba.Pix, rgbaBuf)

			frame := &Frame{
				Image:        rgba,
				Bounds:       image.Rect(0, 0, w, h),
				DisplayIndex: c.config.DisplayIndex,
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

// captureSHM captures a frame using XShm (zero-copy from X server to shared memory).
func (c *x11Capturer) captureSHM(dst []byte, w, h int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cookie := shm.GetImage(
		c.conn,
		xproto.Drawable(c.root),
		0, 0,
		c.width, c.height,
		0xFFFFFFFF, // AllPlanes
		byte(xproto.ImageFormatZPixmap),
		c.shmSeg,
		0,
	)

	_, err := cookie.Reply()
	if err != nil {
		return fmt.Errorf("XShmGetImage failed: %w", err)
	}

	// Use pre-created slice view of the shared memory frame buffer
	bgraToRGBA(c.shmBuf, dst, w, h)

	return nil
}

// captureFallback captures a frame using xproto.GetImage (slower, no SHM).
func (c *x11Capturer) captureFallback(dst []byte, w, h int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cookie := xproto.GetImage(
		c.conn,
		xproto.ImageFormatZPixmap,
		xproto.Drawable(c.root),
		0, 0,
		c.width, c.height,
		0xFFFFFFFF,
	)

	reply, err := cookie.Reply()
	if err != nil {
		return fmt.Errorf("XGetImage failed: %w", err)
	}

	bgraToRGBA(reply.Data, dst, w, h)
	return nil
}

// bgraToRGBA converts BGRA pixel data to RGBA.
// X11 uses BGRA byte order on little-endian systems.
func bgraToRGBA(src, dst []byte, w, h int) {
	total := w * h
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
		syscall.Syscall(syscall.SYS_SHMDT, c.shmAddr, 0, 0)
		c.shmAvailable = false
	}

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// uintptrToPointer converts a uintptr address to unsafe.Pointer.
func uintptrToPointer(ptr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
}


