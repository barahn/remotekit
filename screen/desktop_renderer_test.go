package screen

import (
	"image/color"
	"testing"
	"time"
)

func TestIsBlackFrame(t *testing.T) {
	w, h := 10, 10
	bufSize := w * h * 4

	// Test case 1: Completely black frame (zeros)
	blackBuf := make([]byte, bufSize)
	if !IsBlackFrame(blackBuf, w, h) {
		t.Errorf("IsBlackFrame failed for a completely black buffer")
	}

	// Test case 2: Not black frame (some non-zero values)
	nonBlackBuf := make([]byte, bufSize)
	// Add some white color in the middle
	idx := (5*w + 5) * 4
	nonBlackBuf[idx] = 255
	nonBlackBuf[idx+1] = 255
	nonBlackBuf[idx+2] = 255
	if IsBlackFrame(nonBlackBuf, w, h) {
		t.Errorf("IsBlackFrame failed for a non-black buffer")
	}

	// Test case 3: Buffer too small
	smallBuf := make([]byte, bufSize-1)
	if !IsBlackFrame(smallBuf, w, h) {
		t.Errorf("IsBlackFrame failed for a small buffer, should return true to avoid panic")
	}
}

func TestVirtualDesktopState_UpdateCursor(t *testing.T) {
	state := &VirtualDesktopState{}

	// Test normal update within bounds
	state.UpdateCursor(0.5, 0.5, 100, 100)
	if state.CurX != 50 || state.CurY != 50 {
		t.Errorf("UpdateCursor expected (50, 50), got (%d, %d)", state.CurX, state.CurY)
	}

	// Test negative bounds
	state.UpdateCursor(-0.1, -0.1, 100, 100)
	if state.CurX != 0 || state.CurY != 0 {
		t.Errorf("UpdateCursor expected (0, 0), got (%d, %d)", state.CurX, state.CurY)
	}

	// Test exceeding bounds
	state.UpdateCursor(1.1, 1.1, 100, 100)
	if state.CurX != 99 || state.CurY != 99 {
		t.Errorf("UpdateCursor expected (99, 99), got (%d, %d)", state.CurX, state.CurY)
	}
}

func TestGenerateTestDesktopImage(t *testing.T) {
	w, h := 1920, 1080
	img := GenerateTestDesktopImage(w, h, "linux", "testhost", "agent123", 1, time.Now(), 500, 500)

	if img == nil {
		t.Fatalf("GenerateTestDesktopImage returned nil")
	}

	bounds := img.Bounds()
	if bounds.Dx() != w || bounds.Dy() != h {
		t.Errorf("Expected dimensions %dx%d, got %dx%d", w, h, bounds.Dx(), bounds.Dy())
	}

	// Simply verify that the buffer has expected size
	expectedLen := w * h * 4
	if len(img.Pix) != expectedLen {
		t.Errorf("Expected image buffer of length %d, got %d", expectedLen, len(img.Pix))
	}
}

func TestRenderDesktop(t *testing.T) {
	w, h := 800, 600
	buf := make([]byte, w*h*4)

	// Initially black
	if !IsBlackFrame(buf, w, h) {
		t.Errorf("Initial buffer should be black")
	}

	RenderDesktop(buf, w, h, "windows", "winhost", "agent456", 2, time.Now(), 400, 300)

	// After rendering, it should not be entirely black
	if IsBlackFrame(buf, w, h) {
		t.Errorf("RenderDesktop failed to draw anything (buffer still black)")
	}
}

func TestRenderDesktop_BufferTooSmall(t *testing.T) {
	w, h := 100, 100
	buf := make([]byte, (w*h*4)-1) // Intentionally too small

	// Should not panic
	RenderDesktop(buf, w, h, "linux", "host", "agent", 1, time.Now(), 0, 0)
}

func TestVirtualDesktopState_HandleClickAndKey(t *testing.T) {
	state := GetGlobalDesktopState()

	// Test clicking logic
	state.HandleClick(0.5, 0.5, 1, 1920, 1080)
	if state.ClickX != 960 || state.ClickY != 540 {
		t.Errorf("HandleClick expected (960, 540), got (%d, %d)", state.ClickX, state.ClickY)
	}
	if state.ClickButton != 1 {
		t.Errorf("HandleClick expected button 1, got %d", state.ClickButton)
	}

	// Test basic key input
	state.InputBuffer = ""
	state.HandleKey("a", "", "linux", "host")
	if state.InputBuffer != "a" {
		t.Errorf("HandleKey 'a', expected buffer 'a', got '%s'", state.InputBuffer)
	}

	state.HandleKey("Backspace", "", "linux", "host")
	if state.InputBuffer != "" {
		t.Errorf("HandleKey Backspace, expected empty buffer, got '%s'", state.InputBuffer)
	}

	state.InputBuffer = "he"
	state.HandleKey("Tab", "", "linux", "host")
	if state.InputBuffer != "help" {
		t.Errorf("HandleKey Tab auto-complete, expected 'help', got '%s'", state.InputBuffer)
	}

	state.HandleKey("Enter", "", "linux", "host")
	if state.InputBuffer != "" {
		t.Errorf("HandleKey Enter, expected empty buffer, got '%s'", state.InputBuffer)
	}
}

func TestDrawHelpers(t *testing.T) {
	// Let's just make sure draw functions don't panic and respect boundaries
	w, h := 100, 100
	dst := make([]byte, w*h*4)

	c := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	drawRect(dst, w, -10, -10, 50, 50, c)
	drawRectOutline(dst, w, 80, 80, 50, 50, c)
	drawCircle(dst, w, 50, 50, 10, c)
	drawText(dst, w, -5, -5, "Hello", c, 1)
	drawCursor(dst, w, 95, 95)

	// Should not have panicked
}
