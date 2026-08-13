//go:build windows

package clipboard

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")

	procGlobalAlloc  = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procGlobalFree   = modKernel32.NewProc("GlobalFree")
)

// WindowsClipboard implements system clipboard operations on Windows via Win32 API.
type WindowsClipboard struct {
	mu             sync.RWMutex
	memoryFallback string
}

// NewManager returns a new Windows clipboard manager.
func NewManager() Manager {
	return &WindowsClipboard{}
}

// openClipboardWithRetry attempts to open the clipboard, retrying up to 5 times.
func openClipboardWithRetry() error {
	for i := 0; i < 5; i++ {
		r, _, _ := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("failed to open clipboard (locked by another process)")
}

// GetText retrieves text from the Windows clipboard.
func (wc *WindowsClipboard) GetText(ctx context.Context) (string, error) {
	if err := openClipboardWithRetry(); err != nil {
		wc.mu.RLock()
		defer wc.mu.RUnlock()
		return wc.memoryFallback, nil
	}
	defer procCloseClipboard.Call()

	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(cfUnicodeText))
	if r == 0 {
		wc.mu.RLock()
		defer wc.mu.RUnlock()
		return wc.memoryFallback, nil
	}

	hData, _, _ := procGetClipboardData.Call(uintptr(cfUnicodeText))
	if hData == 0 {
		wc.mu.RLock()
		defer wc.mu.RUnlock()
		return wc.memoryFallback, nil
	}

	ptr, _, _ := procGlobalLock.Call(hData)
	if ptr == 0 {
		wc.mu.RLock()
		defer wc.mu.RUnlock()
		return wc.memoryFallback, nil
	}
	defer procGlobalUnlock.Call(hData)

	// Read UTF-16 characters until null terminator
	var utf16Slice []uint16
	p := (*uint16)(unsafe.Pointer(ptr)) // #nosec G103 -- required for Win32 clipboard memory access
	for {
		val := *p
		if val == 0 {
			break
		}
		utf16Slice = append(utf16Slice, val)
		p = (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 2)) // #nosec G103 -- stride 2 bytes for UTF-16
	}

	text := syscall.UTF16ToString(utf16Slice)

	wc.mu.Lock()
	wc.memoryFallback = text
	wc.mu.Unlock()

	return text, nil
}

// SetText sets text on the Windows clipboard.
func (wc *WindowsClipboard) SetText(ctx context.Context, text string) error {
	wc.mu.Lock()
	wc.memoryFallback = text
	wc.mu.Unlock()

	utf16Chars, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	byteLen := len(utf16Chars) * 2

	hGlobal, _, _ := procGlobalAlloc.Call(uintptr(gmemMoveable), uintptr(byteLen))
	if hGlobal == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}

	ptr, _, _ := procGlobalLock.Call(hGlobal)
	if ptr == 0 {
		_, _, _ = procGlobalFree.Call(hGlobal)
		return fmt.Errorf("GlobalLock failed")
	}

	destSlice := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16Chars)) // #nosec G103 -- Win32 memory copy
	copy(destSlice, utf16Chars)

	_, _, _ = procGlobalUnlock.Call(hGlobal)

	if err := openClipboardWithRetry(); err != nil {
		_, _, _ = procGlobalFree.Call(hGlobal)
		return err
	}
	defer procCloseClipboard.Call()

	_, _, _ = procEmptyClipboard.Call()

	r, _, _ := procSetClipboardData.Call(uintptr(cfUnicodeText), hGlobal)
	if r == 0 {
		_, _, _ = procGlobalFree.Call(hGlobal)
		return fmt.Errorf("SetClipboardData failed")
	}

	// System takes ownership of hGlobal upon successful SetClipboardData
	return nil
}
