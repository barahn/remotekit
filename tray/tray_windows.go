// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package tray

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/barahn/remotekit/service"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSetForeground            = user32.NewProc("SetForegroundWindow")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procLoadIconW                = user32.NewProc("LoadIconW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetAncestor              = user32.NewProc("GetAncestor")
	procGetParent                = user32.NewProc("GetParent")

	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow         = kernel32.NewProc("GetConsoleWindow")
	procGetCurrentProcessId      = kernel32.NewProc("GetCurrentProcessId")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procCloseHandle              = kernel32.NewProc("CloseHandle")

	wndProcCallback        uintptr
	enumWindowsCallback    uintptr
	enumWindowsMu          sync.Mutex
	enumWindowsFoundMap    map[uintptr]bool
	enumWindowsRelatedPIDs map[uint32]bool
)

func init() {
	wndProcCallback = syscall.NewCallback(wndProc)
	enumWindowsCallback = syscall.NewCallback(enumWindowsProc)
}

func enumWindowsProc(hwnd uintptr, lParam uintptr) uintptr {
	var winPid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&winPid)))

	var length uintptr
	length, _, _ = procGetWindowTextLengthW.Call(hwnd)
	if length > 0 {
		buf := make([]uint16, length+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), length+1)
		title := strings.ToLower(syscall.UTF16ToString(buf))
		if strings.Contains(title, "barahn") {
			if enumWindowsFoundMap != nil {
				enumWindowsFoundMap[hwnd] = true
			}
		}
	}

	if enumWindowsRelatedPIDs != nil && enumWindowsRelatedPIDs[winPid] {
		var classNameBuf [256]uint16
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&classNameBuf[0])), 256)
		className := syscall.UTF16ToString(classNameBuf[:])

		if className == "CASCADIA_HOSTING_WINDOW_CLASS" || className == "ConsoleWindowClass" || className == "PseudoConsoleWindow" {
			if enumWindowsFoundMap != nil {
				enumWindowsFoundMap[hwnd] = true
			}
		}
	}
	return 1
}

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

	mfString    = 0x00000000
	mfDisabled  = 0x00000002
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002

	cmdHeader        = 1000
	cmdStatus        = 1001
	cmdID            = 1002
	cmdCopyID        = 1003
	cmdServer        = 1004
	cmdToggleLogs    = 1006
	cmdToggleService = 1007
	cmdExit          = 1008
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
	cbSize            uint32
	_                 uint32 // 64-bit padding
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	_                 uint32 // 64-bit padding
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          [16]byte
	hBalloonIcon      uintptr
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
		status:     "Connecting...",
		online:     false,
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
		t.nid.uFlags = nifMessage | nifIcon | nifTip
		res, _, err = procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&t.nid)))
		if res != 0 {
			log.Println("[Tray] System tray icon registered successfully.")
		} else {
			log.Printf("[Tray] Warning: Shell_NotifyIcon failed: %v\n", err)
		}
	} else {
		log.Println("[Tray] System tray icon registered successfully.")
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

type processEntry32W struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

func getRelatedProcessIDs() map[uint32]bool {
	pids := make(map[uint32]bool)
	curPid, _, _ := procGetCurrentProcessId.Call()
	if curPid == 0 {
		return pids
	}
	pids[uint32(curPid)] = true

	snap, _, _ := procCreateToolhelp32Snapshot.Call(0x00000002 /* TH32CS_SNAPPROCESS */, 0)
	if snap == 0 || snap == uintptr(syscall.InvalidHandle) {
		return pids
	}
	defer procCloseHandle.Call(snap)

	parentMap := make(map[uint32]uint32)
	var pe processEntry32W
	pe.dwSize = uint32(unsafe.Sizeof(pe))

	ret, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	for ret != 0 {
		parentMap[pe.th32ProcessID] = pe.th32ParentProcessID
		ret, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}

	curr := uint32(curPid)
	for i := 0; i < 5; i++ {
		parent, ok := parentMap[curr]
		if !ok || parent == 0 || parent == curr {
			break
		}
		pids[parent] = true
		curr = parent
	}

	return pids
}

// Global cached console window handles to ensure clean show/hide toggling
var (
	cachedConsoleHwndsMu sync.Mutex
	cachedConsoleHwnds   []uintptr
)

func getConsoleWindows() []uintptr {
	cachedConsoleHwndsMu.Lock()
	defer cachedConsoleHwndsMu.Unlock()

	foundMap := make(map[uintptr]bool)
	for _, h := range cachedConsoleHwnds {
		if h != 0 {
			foundMap[h] = true
		}
	}

	// 1. Direct console window
	consoleHwnd, _, _ := procGetConsoleWindow.Call()
	if consoleHwnd != 0 {
		foundMap[consoleHwnd] = true

		rootHwnd, _, _ := procGetAncestor.Call(consoleHwnd, 2 /* GA_ROOT */)
		if rootHwnd != 0 {
			foundMap[rootHwnd] = true
		}

		ownerHwnd, _, _ := procGetAncestor.Call(consoleHwnd, 3 /* GA_ROOTOWNER */)
		if ownerHwnd != 0 {
			foundMap[ownerHwnd] = true
		}

		parentHwnd, _, _ := procGetParent.Call(consoleHwnd)
		if parentHwnd != 0 {
			foundMap[parentHwnd] = true
		}
	}

	// 2. Enumerate all top-level windows matching title or process lineage
	enumWindowsMu.Lock()
	enumWindowsFoundMap = foundMap
	enumWindowsRelatedPIDs = getRelatedProcessIDs()
	procEnumWindows.Call(enumWindowsCallback, 0)
	enumWindowsFoundMap = nil
	enumWindowsRelatedPIDs = nil
	enumWindowsMu.Unlock()

	var hwnds []uintptr
	for h := range foundMap {
		if h != 0 {
			hwnds = append(hwnds, h)
		}
	}

	cachedConsoleHwnds = hwnds
	return hwnds
}

// HideConsoleWindow hides all console and terminal windows associated with the agent.
func HideConsoleWindow() {
	for _, h := range getConsoleWindows() {
		procShowWindow.Call(h, 0 /* SW_HIDE */)
	}
}

// ShowConsoleWindow restores and brings to foreground all console and terminal windows.
func ShowConsoleWindow() {
	for _, h := range getConsoleWindows() {
		procShowWindow.Call(h, 9 /* SW_RESTORE */)
		procShowWindow.Call(h, 5 /* SW_SHOW */)
		procSetForeground.Call(h)
	}
}

// IsConsoleVisible returns true if any console or terminal window is currently visible.
func IsConsoleVisible() bool {
	windows := getConsoleWindows()
	if len(windows) == 0 {
		return false
	}
	for _, h := range windows {
		vis, _, _ := procIsWindowVisible.Call(h)
		if vis != 0 {
			return true
		}
	}
	return false
}

// ToggleConsoleWindow toggles visibility between hidden and visible.
func ToggleConsoleWindow() {
	if IsConsoleVisible() {
		HideConsoleWindow()
	} else {
		ShowConsoleWindow()
	}
}

func hideConsoleWindow() {
	HideConsoleWindow()
}

func showConsoleWindow() {
	ShowConsoleWindow()
}

func isConsoleVisible() bool {
	return IsConsoleVisible()
}

func toggleConsoleWindow() {
	ToggleConsoleWindow()
}

func (t *windowsTrayManager) toggleWindowsService() {
	svcMgr := service.NewServiceManager("BarahnAgent")
	status, err := svcMgr.Status()
	if err == nil && status.Installed {
		_ = svcMgr.Stop()
		if err := svcMgr.Uninstall(); err != nil {
			t.showBalloonMessage("Barahn Service Error", fmt.Sprintf("Failed to remove service: %v", err))
			return
		}
		t.showBalloonMessage("Barahn Service Removed", "Windows background service uninstalled successfully.")
	} else {
		exePath, err := os.Executable()
		if err != nil {
			t.showBalloonMessage("Barahn Service Error", "Failed to detect executable path.")
			return
		}
		exePath, _ = filepath.Abs(exePath)
		configPath := "agent.pem"
		if cand, err := filepath.Abs("agent.pem"); err == nil {
			if _, err := os.Stat(cand); err == nil {
				configPath = cand
			}
		}

		cfg := service.Config{
			Name:        "BarahnAgent",
			DisplayName: "Barahn Remote Access Agent",
			Description: "Barahn persistent unattended remote support and management agent daemon.",
			ExecPath:    exePath,
			Args:        service.BuildServiceArgs(t.serverAddr, configPath, true),
			AutoStart:   true,
		}

		if err := svcMgr.Install(cfg); err != nil {
			t.showBalloonMessage("Barahn Service Error", fmt.Sprintf("Installation failed: %v", err))
			return
		}
		_ = svcMgr.Start()
		t.showBalloonMessage("Barahn Service Active", "Barahn is now running as an unattended Windows Service (Auto-Start enabled).")
	}
}

func (t *windowsTrayManager) showBalloonMessage(title, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	copyStringToUtf16Slice(title, t.nid.szInfoTitle[:])
	copyStringToUtf16Slice(message, t.nid.szInfo[:])
	t.nid.uFlags = nifInfo | nifTip | nifIcon | nifMessage
	t.nid.dwInfoFlags = 0x00000001 /* NIIF_INFO */
	procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&t.nid)))
}

func wndProc(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	switch message {
	case wmTrayIcon:
		switch lParam {
		case wmRButtonUp, wmContextMenu:
			showContextMenu(hwnd)
		case wmLButtonDbl:
			toggleConsoleWindow()
		}
		return 0

	case wmCommand:
		cmdIDVal := int(wParam & 0xFFFF)
		switch cmdIDVal {
		case cmdCopyID:
			if activeTray != nil {
				activeTray.copyIDToClipboard()
			}
		case cmdToggleLogs:
			toggleConsoleWindow()
		case cmdToggleService:
			if activeTray != nil {
				go activeTray.toggleWindowsService()
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

	case 0x0002 /* WM_DESTROY */ :
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

	headerText, _ := syscall.UTF16PtrFromString("Barahn Endpoint Agent")
	statusText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("Status: %s", activeTray.status))
	idText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("ID: %s", activeTray.agentID))
	serverText, _ := syscall.UTF16PtrFromString(fmt.Sprintf("Server: %s", activeTray.serverAddr))
	copyText, _ := syscall.UTF16PtrFromString("Copy Endpoint ID")

	var logActionText string
	if isConsoleVisible() {
		logActionText = "Hide Console Logs"
	} else {
		logActionText = "View Console Logs"
	}
	logText, _ := syscall.UTF16PtrFromString(logActionText)

	// Check if Windows Service is installed
	svcMgr := service.NewServiceManager("BarahnAgent")
	svcStatus, _ := svcMgr.Status()
	var svcActionText string
	if svcStatus.Installed {
		svcActionText = "Uninstall Windows Service"
	} else {
		svcActionText = "Install as Windows Service (Auto-Start)"
	}
	svcText, _ := syscall.UTF16PtrFromString(svcActionText)

	exitText, _ := syscall.UTF16PtrFromString("Exit Agent")

	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdHeader), uintptr(unsafe.Pointer(headerText)))
	procAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdStatus), uintptr(unsafe.Pointer(statusText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdID), uintptr(unsafe.Pointer(idText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString|mfDisabled), uintptr(cmdServer), uintptr(unsafe.Pointer(serverText)))
	procAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdCopyID), uintptr(unsafe.Pointer(copyText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdToggleLogs), uintptr(unsafe.Pointer(logText)))
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdToggleService), uintptr(unsafe.Pointer(svcText)))
	procAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdExit), uintptr(unsafe.Pointer(exitText)))

	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	procSetForeground.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, uintptr(tpmRightButton), uintptr(p.x), uintptr(p.y), 0, hwnd, 0)
}

func (t *windowsTrayManager) copyIDToClipboard() {
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
