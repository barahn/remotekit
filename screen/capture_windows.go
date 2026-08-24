//go:build windows

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
)

const (
	srccopy    = 0x00CC0020
	captureBlt = 0x40000000
	biRGB      = 0
	dibRGBCols = 0
)

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")
	modGdi32  = syscall.NewLazyDLL("gdi32.dll")
	modShcore = syscall.NewLazyDLL("shcore.dll")

	procGetDC                         = modUser32.NewProc("GetDC")
	procReleaseDC                     = modUser32.NewProc("ReleaseDC")
	procGetSystemMetrics              = modUser32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors           = modUser32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = modUser32.NewProc("GetMonitorInfoW")
	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = modUser32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwareness        = modShcore.NewProc("SetProcessDpiAwareness")

	procCreateCompatibleDC     = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = modGdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = modGdi32.NewProc("SelectObject")
	procBitBlt                 = modGdi32.NewProc("BitBlt")
	procGetDIBits              = modGdi32.NewProc("GetDIBits")
	procDeleteDC               = modGdi32.NewProc("DeleteDC")
	procDeleteObject           = modGdi32.NewProc("DeleteObject")
	procChangeDisplaySettingsW = modUser32.NewProc("ChangeDisplaySettingsW")
)

var initDPIOnce sync.Once

// initDPIAwareness enables Per-Monitor DPI awareness on Windows so GetSystemMetrics and BitBlt operate on physical pixels.
func initDPIAwareness() {
	initDPIOnce.Do(func() {
		// 1. Windows 10 1607+ (Per-Monitor V2: -4)
		if procSetProcessDpiAwarenessContext.Find() == nil {
			dpiContext := ^uintptr(3) // -4
			ret, _, _ := procSetProcessDpiAwarenessContext.Call(dpiContext)
			if ret != 0 {
				return
			}
		}
		// 2. Windows 8.1+ (PROCESS_PER_MONITOR_DPI_AWARE = 2)
		if procSetProcessDpiAwareness.Find() == nil {
			ret, _, _ := procSetProcessDpiAwareness.Call(2)
			if ret == 0 {
				return
			}
		}
		// 3. Windows Vista+ System DPI Aware
		if procSetProcessDPIAware.Find() == nil {
			_, _, _ = procSetProcessDPIAware.Call()
		}
	})
}

var (
	enumDisplayMonitorsMu       sync.Mutex
	enumDisplayMonitorsSlice    []Display
	enumDisplayMonitorsCallback uintptr
)

func enumDisplayMonitorsProc(hMonitor, hdcMonitor, lprcMonitor, dwData uintptr) uintptr {
	r := (*rect)(unsafe.Pointer(lprcMonitor)) // #nosec G103 -- Win32 callback struct
	idx := len(enumDisplayMonitorsSlice)
	name := fmt.Sprintf("Display %d (%dx%d)", idx+1, r.right-r.left, r.bottom-r.top)
	primary := (r.left == 0 && r.top == 0)

	enumDisplayMonitorsSlice = append(enumDisplayMonitorsSlice, Display{
		Index:   idx,
		Name:    name,
		Bounds:  image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom)),
		Primary: primary,
	})
	return 1 // Continue enumeration
}

func init() {
	initDPIAwareness()
	enumDisplayMonitorsCallback = syscall.NewCallback(enumDisplayMonitorsProc)
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
	bmiColors [1]uint32
}

type rect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

type monitorInfoEx struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
	szDevice  [32]uint16
}

type windowsCapturer struct {
	config   CaptureConfig
	mu       sync.Mutex
	frames   chan *Frame
	running  atomic.Bool
	seqNum   atomic.Uint64
	stopOnce sync.Once
	cancel   context.CancelFunc

	width  int
	height int
}

type devModeW struct {
	dmDeviceName       [32]uint16
	dmSpecVersion      uint16
	dmDriverVersion    uint16
	dmSize             uint16
	dmDriverExtra      uint16
	dmFields           uint32
	dmOrientation      int16
	dmPaperSize        int16
	dmPaperLength      int16
	dmPaperWidth       int16
	dmScale            int16
	dmCopies           int16
	dmDefaultSource    int16
	dmPrintQuality     int16
	dmColor            int16
	dmDuplex           int16
	dmYResolution      int16
	dmTTOption         int16
	dmCollate          int16
	dmFormName         [32]uint16
	dmLogPixels        uint16
	dmBitsPerPel       uint32
	dmPelsWidth        uint32
	dmPelsHeight       uint32
	dmDisplayFlags     uint32
	dmDisplayFrequency uint32
	dmICMMethod        uint32
	dmICMIntent        uint32
	dmMediaType        uint32
	dmDitherType       uint32
	dmReserved1        uint32
	dmReserved2        uint32
	dmPanningWidth     uint32
	dmPanningHeight    uint32
}

// TrySetDisplayResolution attempts to programmatically adjust the Windows display resolution to target bounds (e.g. 1920x1080).
func TrySetDisplayResolution(targetW, targetH int) bool {
	if procChangeDisplaySettingsW.Find() != nil {
		return false
	}
	var dm devModeW
	dm.dmSize = uint16(unsafe.Sizeof(dm))
	dm.dmFields = 0x00080000 /* DM_PELSWIDTH */ | 0x00100000 /* DM_PELSHEIGHT */
	dm.dmPelsWidth = uint32(targetW)
	dm.dmPelsHeight = uint32(targetH)

	ret, _, _ := procChangeDisplaySettingsW.Call(uintptr(unsafe.Pointer(&dm)), 0)
	return ret == 0 /* DISP_CHANGE_SUCCESSFUL */
}

// NewCapturer creates a Windows screen capturer using the Win32 GDI / DirectX APIs.
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

	cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	w := int(cx)
	h := int(cy)

	// If current display resolution is below standard FullHD, attempt to auto-negotiate to 1080p
	if w < 1920 && w > 0 {
		if TrySetDisplayResolution(1920, 1080) {
			cx, _, _ = procGetSystemMetrics.Call(0)
			cy, _, _ = procGetSystemMetrics.Call(1)
			w = int(cx)
			h = int(cy)
		}
	}

	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}

	return &windowsCapturer{
		config: config,
		width:  w,
		height: h,
	}, nil
}

func (c *windowsCapturer) Displays() ([]Display, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	enumDisplayMonitorsMu.Lock()
	defer enumDisplayMonitorsMu.Unlock()

	enumDisplayMonitorsSlice = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, enumDisplayMonitorsCallback, 0)
	displays := make([]Display, len(enumDisplayMonitorsSlice))
	copy(displays, enumDisplayMonitorsSlice)
	enumDisplayMonitorsSlice = nil

	if len(displays) == 0 {
		// Fallback to primary screen
		displays = append(displays, Display{
			Index:   0,
			Name:    "Windows Primary Display",
			Bounds:  image.Rect(0, 0, c.width, c.height),
			Primary: true,
		})
	}

	return displays, nil
}

func (c *windowsCapturer) Start(ctx context.Context) error {
	if c.running.Load() {
		return ErrAlreadyStarted
	}

	c.frames = make(chan *Frame, c.config.FrameBufferSize)
	c.running.Store(true)
	c.seqNum.Store(0)
	c.stopOnce = sync.Once{}

	captureCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	go c.captureLoop(captureCtx)

	return nil
}

func (c *windowsCapturer) Frames() <-chan *Frame {
	return c.frames
}

func (c *windowsCapturer) Stop() {
	c.stopOnce.Do(func() {
		c.running.Store(false)
		if c.cancel != nil {
			c.cancel()
		}
	})
}

func (c *windowsCapturer) SetDisplay(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if index < 0 {
		return ErrDisplayNotFound
	}
	c.config.DisplayIndex = index
	return nil
}

func (c *windowsCapturer) Close() {
	c.Stop()
}

func (c *windowsCapturer) getActiveDisplayBounds() (image.Rectangle, int, int) {
	c.mu.Lock()
	dispIdx := c.config.DisplayIndex
	c.mu.Unlock()

	displays, err := c.Displays()
	if err == nil && dispIdx >= 0 && dispIdx < len(displays) {
		b := displays[dispIdx].Bounds
		return image.Rect(0, 0, b.Dx(), b.Dy()), b.Min.X, b.Min.Y
	}
	cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	w := int(cx)
	h := int(cy)
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}
	return image.Rect(0, 0, w, h), 0, 0
}

func (c *windowsCapturer) captureLoop(ctx context.Context) {
	defer close(c.frames)

	interval := time.Second / time.Duration(c.config.TargetFPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var bgraBuf []byte
	var rgbaBuf []byte
	var currentW, currentH int

	var bmi bitmapInfo
	bmi.bmiHeader.biSize = uint32(unsafe.Sizeof(bmi.bmiHeader))
	bmi.bmiHeader.biPlanes = 1
	bmi.bmiHeader.biBitCount = 32
	bmi.bmiHeader.biCompression = biRGB

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.running.Load() {
				return
			}

			bounds, srcX, srcY := c.getActiveDisplayBounds()
			w := bounds.Dx()
			h := bounds.Dy()
			if w <= 0 {
				w = 1920
			}
			if h <= 0 {
				h = 1080
			}

			if w != currentW || h != currentH || len(bgraBuf) != w*h*4 {
				currentW = w
				currentH = h
				bgraBuf = make([]byte, w*h*4)
				rgbaBuf = make([]byte, w*h*4)
				bmi.bmiHeader.biWidth = int32(w)
				bmi.bmiHeader.biHeight = -int32(h) // Negative for top-down DIB
			}

			err := c.captureFrame(bgraBuf, &bmi, srcX, srcY, w, h)
			if err != nil {
				continue
			}

			bgraToRGBA(bgraBuf, rgbaBuf, w, h)

			rgba := image.NewRGBA(image.Rect(0, 0, w, h))
			copy(rgba.Pix, rgbaBuf)

			c.mu.Lock()
			currentDispIdx := c.config.DisplayIndex
			c.mu.Unlock()

			frame := &Frame{
				Image:        rgba,
				Bounds:       image.Rect(0, 0, w, h),
				DisplayIndex: currentDispIdx,
				CapturedAt:   time.Now(),
				SequenceNum:  c.seqNum.Add(1),
			}

			select {
			case c.frames <- frame:
			default:
				// Dropped frame for slow consumer
			}
		}
	}
}

func (c *windowsCapturer) captureFrame(dst []byte, bmi *bitmapInfo, srcX, srcY, w, h int) error {
	hDesktopDC, _, _ := procGetDC.Call(0)
	if hDesktopDC == 0 {
		return fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, hDesktopDC)

	hMemDC, _, _ := procCreateCompatibleDC.Call(hDesktopDC)
	if hMemDC == 0 {
		return fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(hMemDC)

	hBitmap, _, _ := procCreateCompatibleBitmap.Call(hDesktopDC, uintptr(w), uintptr(h))
	if hBitmap == 0 {
		return fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hBitmap)

	hOldBmp, _, _ := procSelectObject.Call(hMemDC, hBitmap)
	defer procSelectObject.Call(hMemDC, hOldBmp)

	r, _, _ := procBitBlt.Call(
		hMemDC, 0, 0, uintptr(w), uintptr(h),
		hDesktopDC, uintptr(srcX), uintptr(srcY), uintptr(srccopy|captureBlt),
	)
	if r == 0 {
		// Retry without CAPTUREBLT if layered windows capture fails
		r, _, _ = procBitBlt.Call(
			hMemDC, 0, 0, uintptr(w), uintptr(h),
			hDesktopDC, uintptr(srcX), uintptr(srcY), uintptr(srccopy),
		)
		if r == 0 {
			return fmt.Errorf("BitBlt failed")
		}
	}

	r, _, _ = procGetDIBits.Call(
		hMemDC, hBitmap, 0, uintptr(h),
		uintptr(unsafe.Pointer(&dst[0])), // #nosec G103 -- GetDIBits buffer
		uintptr(unsafe.Pointer(bmi)),     // #nosec G103 -- BITMAPINFO struct
		uintptr(dibRGBCols),
	)
	if r == 0 {
		return fmt.Errorf("GetDIBits failed")
	}

	return nil
}

func bgraToRGBA(src, dst []byte, w, h int) {
	total := w * h
	for i := 0; i < total; i++ {
		si := i * 4
		dst[si+0] = src[si+2] // R <- B
		dst[si+1] = src[si+1] // G <- G
		dst[si+2] = src[si+0] // B <- R
		dst[si+3] = 255       // A
	}
}
