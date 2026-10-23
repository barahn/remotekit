// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package clipboard

import (
	"context"
	"runtime"
	"syscall"
	"unsafe"
)

var procRegisterClipboardFormatW = modUser32.NewProc("RegisterClipboardFormatW")

// Concealed reports whether the clipboard holds what a password manager
// marked to keep out of monitors and history (concealedByWindowsFormats).
func (wc *WindowsClipboard) Concealed(ctx context.Context) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := openClipboardWithRetry(); err != nil {
		return false, err
	}
	defer procCloseClipboard.Call()

	has := func(name string) bool {
		f := registeredFormat(name)
		if f == 0 {
			return false
		}
		r, _, _ := procIsClipboardFormatAvailable.Call(f)
		return r != 0
	}
	dword := func(name string) (uint32, bool) {
		f := registeredFormat(name)
		if f == 0 {
			return 0, false
		}
		h, _, _ := procGetClipboardData.Call(f)
		if h == 0 {
			return 0, false
		}
		ptr, _, _ := procGlobalLock.Call(h)
		if ptr == 0 {
			return 0, false
		}
		defer procGlobalUnlock.Call(h)
		// winPtr is safe here because h is locked via GlobalLock, guaranteeing
		// the 4-byte DWORD memory remains valid until procGlobalUnlock is called on return.
		return *(*uint32)(winPtr(ptr)), true // #nosec G103 -- a DWORD in clipboard memory
	}
	return concealedByWindowsFormats(has, dword), nil
}

// registeredFormat is the clipboard format number Windows gives name, or 0.
func registeredFormat(name string) uintptr {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p))) // #nosec G103 -- Win32 string argument
	return f
}
