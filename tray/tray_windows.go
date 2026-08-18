//go:build windows

package tray

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procLoadIconW        = user32.NewProc("LoadIconW")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmUser        = 0x0400
	wmTrayIcon    = wmUser + 100
	wmCommand     = 0x0111
	wmRButtonUp   = 0x0205
	wmLButtonDbl  = 0x0203
	wmContextMenu = 0x007B

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	mfString   = 0x00000000
	mfDisabled = 0x00000002
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002

	cmdHeader = 1000
	cmdStatus = 1001
	cmdID     = 1002
	cmdCopyID = 1003
	cmdServer = 1004
	cmdExit   = 1005
)

type point struct {
	x, y int32
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconDataW struct {
	cbSize           uint32
	_                uint32 // 64-bit padding
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	_                uint32 // 64-bit padding
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type windowsTrayManager struct {
	agentID    string
	serverAddr string
	status     string
	online     bool
	onExit     func()

	mu      sync.Mutex
	hwnd    uintptr
	nid     notifyIconDataW
	running bool
}

// Global active instance for wndproc dispatch
var activeTray *windowsTrayManager

// NewTrayManager creates a Windows system tray icon for the Barahn Agent.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	return &windowsTrayManager{
		agentID:    agentID,
		serverAddr: serverAddr,
		status:     "Online",
		online:     true,
		onExit:     onExit,
	}
}

func (t *windowsTrayManager) Start(ctx context.Context) {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	t.running = true
	activeTray = t
	t.mu.Unlock()

	go t.runMessageLoop(ctx)
}

func (t *windowsTrayManager) SetStatus(status string, online bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
	t.online = online

	if t.hwnd != 0 {
		tip := fmt.Sprintf("Barahn Endpoint Agent\nStatus: %s\nID: %s", t.status, t.agentID)
		copyStringToUtf16Slice(tip, t.nid.szTip[:])
		t.nid.uFlags = nifTip
		procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&t.nid)))
	}
}

func (t *windowsTrayManager) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return
	}
	t.running = false
	if t.hwnd != 0 {
		procShellNotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&t.nid)))
		procPostMessageW.Call(t.hwnd, 0x0010 /* WM_CLOSE */, 0, 0)
	}
}

func (t *windowsTrayManager) runMessageLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("BarahnTrayWindowClass")

	wndProcCallback := syscall.NewCallback(wndProc)

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProcCallback,
		hInstance:     hInstance,
		lpszClassName: className,
	}

	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0, 0, 0, 0,
		0, 0,
		hInstance,
		0,
	)

	if hwnd == 0 {
		return
	}

	t.mu.Lock()
	t.hwnd = hwnd

	// Load default executable embedded application icon
	hIcon, _, _ := procLoadIconW.Call(hInstance, uintptr(1))
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(32512) /* IDI_APPLICATION */)
	}

	t.nid = notifyIconDataW{
		cbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:             hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip | nifInfo,
		uCallbackMessage: wmTrayIcon,
		hIcon:            hIcon,
	}

	tip := fmt.Sprintf("Barahn Endpoint Agent\nStatus: %s\nID: %s", t.status, t.agentID)
	copyStringToUtf16Slice(tip, t.nid.szTip[:])

	infoTitle := "Barahn Agent Connected"
	infoMsg := fmt.Sprintf("Endpoint ID: %s\nRemote control daemon active.", t.agentID)
	copyStringToUtf16Slice(infoTitle, t.nid.szInfoTitle[:])
	copyStringToUtf16Slice(infoMsg, t.nid.szInfo[:])
	t.nid.dwInfoFlags = 0x00000001 /* NIIF_INFO */

	res, _, err := procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&t.nid)))
	if res == 0 {
		// Fallback without balloon flags
		t.nid.uFlags = nifMessage | nifIcon | nifTip
		res, _, err = procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&t.nid)))
		if res != 0 {
			fmt.Println("[Tray] System tray icon registered successfully.")
		} else {
			fmt.Printf("[Tray] Warning: Shell_NotifyIcon failed: %v\n", err)
		}
	} else {
		fmt.Println("[Tray] System tray icon registered successfully.")
	}
	t.mu.Unlock()

	go func() {
		<-ctx.Done()
		t.Stop()
	}()

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	switch message {
	case wmTrayIcon:
		switch lParam {
		case wmRButtonUp, wmContextMenu:
			showContextMenu(hwnd)
		case wmLButtonDbl:
			if activeTray != nil {
				activeTray.showBalloon()
			}
		}
		return 0

	case wmCommand:
		cmdIDVal := int(wParam & 0xFFFF)
		switch cmdIDVal {
		case cmdCopyID:
			if activeTray != nil {
				activeTray.copyIDToClipboard()
			}
		case cmdExit:
			if activeTray != nil {
				if activeTray.onExit != nil {
					activeTray.onExit()
				}
				activeTray.Stop()
			}
		}
		return 0

	case 0x0002 /* WM_DESTROY */:
		procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func showContextMenu(hwnd uintptr) {
	if activeTray == nil {
		return
	}

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	headerText, _ := syscall.UTF16PtrFromString("🐕 Barahn Endpoint Agent")
	statusText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("🟢 Status: %s", activeTray.status))
	idText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("🆔 ID: %s", activeTray.agentID))
	serverText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("🌐 Server: %s", activeTray.serverAddr))
	copyText, _ := syscall.UTF16PtrFromString("📋 Copy Endpoint ID")
	exitText, _ := syscall.UTF16PtrFromString("❌ Exit Agent")

	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdHeader), uintptr(unsafe.Pointer(headerText)))
	procAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdStatus), uintptr(unsafe.Pointer(statusText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdID), uintptr(unsafe.Pointer(idText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdServer), uintptr(unsafe.Pointer(serverText)))
	procAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdCopyID), uintptr(unsafe.Pointer(copyText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdExit), uintptr(unsafe.Pointer(exitText)))

	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	procSetForeground.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, uintptr(tpmRightButton), uintptr(p.x), uintptr(p.y), 0, hwnd, 0)
}

func (t *windowsTrayManager) showBalloon() {
	t.mu.Lock()
	defer t.mu.Unlock()

	infoTitle := "Barahn Agent - Active"
	infoMsg := fmt.Sprintf("Status: %s\nID: %s\nConnected to %s", t.status, t.agentID, t.serverAddr)
	copyStringToUtf16Slice(infoTitle, t.nid.szInfoTitle[:])
	copyStringToUtf16Slice(infoMsg, t.nid.szInfo[:])
	t.nid.uFlags = nifInfo
	t.nid.dwInfoFlags = 0x00000001 /* NIIF_INFO */
	procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&t.nid)))
}

func (t *windowsTrayManager) copyIDToClipboard() {
	// Optional clipboard setting using user32
	procOpenClipboard := user32.NewProc("OpenClipboard")
	procEmptyClipboard := user32.NewProc("EmptyClipboard")
	procSetClipboardData := user32.NewProc("SetClipboardData")
	procCloseClipboard := user32.NewProc("CloseClipboard")
	procGlobalAlloc := kernel32.NewProc("GlobalAlloc")
	procGlobalLock := kernel32.NewProc("GlobalLock")
	procGlobalUnlock := kernel32.NewProc("GlobalUnlock")

	ret, _, _ := procOpenClipboard.Call(0)
	if ret == 0 {
		return
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	textBytes := append([]byte(t.agentID), 0)
	hMem, _, _ := procGlobalAlloc.Call(0x0042 /* GMEM_MOVEABLE | GMEM_ZEROINIT */, uintptr(len(textBytes)))
	if hMem == 0 {
		return
	}

	pMem, _, _ := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(pMem)), len(textBytes)), textBytes)
	procGlobalUnlock.Call(hMem)

	procSetClipboardData.Call(1 /* CF_TEXT */, hMem)
}

func copyStringToUtf16Slice(s string, dst []uint16) {
	utf16Chars, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	copy(dst, utf16Chars)
}
