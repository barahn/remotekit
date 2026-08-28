package screen

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"time"
)

// Simple 5x7 bitmap font for rendering telemetry on fallback/simulated desktops without external dependencies.
var font5x7 = map[byte][7]byte{
	' ':  {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	'!':  {0x04, 0x04, 0x04, 0x04, 0x00, 0x00, 0x04},
	'"':  {0x0A, 0x0A, 0x00, 0x00, 0x00, 0x00, 0x00},
	'#':  {0x0A, 0x1F, 0x0A, 0x0A, 0x1F, 0x0A, 0x00},
	'$':  {0x04, 0x0F, 0x14, 0x0E, 0x05, 0x1E, 0x04},
	'%':  {0x19, 0x19, 0x02, 0x04, 0x08, 0x13, 0x13},
	'&':  {0x0C, 0x12, 0x14, 0x08, 0x15, 0x12, 0x0D},
	'\'': {0x04, 0x04, 0x02, 0x00, 0x00, 0x00, 0x00},
	'(':  {0x02, 0x04, 0x08, 0x08, 0x08, 0x04, 0x02},
	')':  {0x08, 0x04, 0x02, 0x02, 0x02, 0x04, 0x08},
	'*':  {0x00, 0x04, 0x15, 0x0E, 0x15, 0x04, 0x00},
	'+':  {0x00, 0x04, 0x04, 0x1F, 0x04, 0x04, 0x00},
	',':  {0x00, 0x00, 0x00, 0x00, 0x0C, 0x04, 0x08},
	'-':  {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
	'.':  {0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C},
	'/':  {0x01, 0x02, 0x04, 0x08, 0x10, 0x00, 0x00},
	'0':  {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1':  {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2':  {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
	'3':  {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4':  {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5':  {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6':  {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7':  {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8':  {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9':  {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	':':  {0x00, 0x0C, 0x0C, 0x00, 0x0C, 0x0C, 0x00},
	';':  {0x00, 0x0C, 0x0C, 0x00, 0x0C, 0x04, 0x08},
	'<':  {0x02, 0x04, 0x08, 0x10, 0x08, 0x04, 0x02},
	'=':  {0x00, 0x1F, 0x00, 0x1F, 0x00, 0x00, 0x00},
	'>':  {0x08, 0x04, 0x02, 0x01, 0x02, 0x04, 0x08},
	'?':  {0x0E, 0x11, 0x01, 0x02, 0x04, 0x00, 0x04},
	'@':  {0x0E, 0x11, 0x01, 0x0D, 0x15, 0x15, 0x0E},
	'A':  {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B':  {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C':  {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D':  {0x1C, 0x12, 0x11, 0x11, 0x11, 0x12, 0x1C},
	'E':  {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F':  {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G':  {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F},
	'H':  {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I':  {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J':  {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K':  {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L':  {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M':  {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N':  {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O':  {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P':  {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q':  {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R':  {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S':  {0x0E, 0x11, 0x10, 0x0E, 0x01, 0x11, 0x0E},
	'T':  {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U':  {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V':  {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W':  {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11},
	'X':  {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y':  {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z':  {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'[':  {0x0E, 0x08, 0x08, 0x08, 0x08, 0x08, 0x0E},
	'\\': {0x10, 0x08, 0x04, 0x02, 0x01, 0x00, 0x00},
	']':  {0x0E, 0x02, 0x02, 0x02, 0x02, 0x02, 0x0E},
	'^':  {0x04, 0x0A, 0x11, 0x00, 0x00, 0x00, 0x00},
	'_':  {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1F},
	'`':  {0x08, 0x04, 0x02, 0x00, 0x00, 0x00, 0x00},
	'a':  {0x00, 0x00, 0x0E, 0x01, 0x0F, 0x11, 0x0F},
	'b':  {0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x1E},
	'c':  {0x00, 0x00, 0x0E, 0x10, 0x10, 0x11, 0x0E},
	'd':  {0x01, 0x01, 0x0D, 0x13, 0x11, 0x11, 0x0F},
	'e':  {0x00, 0x00, 0x0E, 0x11, 0x1F, 0x10, 0x0E},
	'f':  {0x06, 0x09, 0x08, 0x1C, 0x08, 0x08, 0x08},
	'g':  {0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x0E},
	'h':  {0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x11},
	'i':  {0x04, 0x00, 0x0C, 0x04, 0x04, 0x04, 0x0E},
	'j':  {0x02, 0x00, 0x06, 0x02, 0x02, 0x12, 0x0C},
	'k':  {0x10, 0x10, 0x12, 0x14, 0x18, 0x14, 0x12},
	'l':  {0x0C, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'm':  {0x00, 0x00, 0x1A, 0x15, 0x15, 0x11, 0x11},
	'n':  {0x00, 0x00, 0x16, 0x19, 0x11, 0x11, 0x11},
	'o':  {0x00, 0x00, 0x0E, 0x11, 0x11, 0x11, 0x0E},
	'p':  {0x00, 0x00, 0x1E, 0x11, 0x1E, 0x10, 0x10},
	'q':  {0x00, 0x00, 0x0D, 0x13, 0x0F, 0x01, 0x01},
	'r':  {0x00, 0x00, 0x16, 0x19, 0x10, 0x10, 0x10},
	's':  {0x00, 0x00, 0x0E, 0x10, 0x0E, 0x01, 0x1E},
	't':  {0x08, 0x08, 0x1C, 0x08, 0x08, 0x09, 0x06},
	'u':  {0x00, 0x00, 0x11, 0x11, 0x11, 0x13, 0x0D},
	'v':  {0x00, 0x00, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'w':  {0x00, 0x00, 0x11, 0x11, 0x15, 0x15, 0x0A},
	'x':  {0x00, 0x00, 0x11, 0x0A, 0x04, 0x0A, 0x11},
	'y':  {0x00, 0x00, 0x11, 0x11, 0x0F, 0x01, 0x0E},
	'z':  {0x00, 0x00, 0x1F, 0x02, 0x04, 0x08, 0x1F},
	'{':  {0x02, 0x04, 0x04, 0x08, 0x04, 0x04, 0x02},
	'|':  {0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'}':  {0x08, 0x04, 0x04, 0x02, 0x04, 0x04, 0x08},
	'~':  {0x00, 0x00, 0x08, 0x15, 0x02, 0x00, 0x00},
}

// VirtualDesktopState holds dynamic interactive state for simulated container desktops.
type VirtualDesktopState struct {
	mu           sync.Mutex
	CurX, CurY   int
	ClickTime    time.Time
	ClickX       int
	ClickY       int
	ClickButton  int
	InputBuffer  string
	HistoryLines []string
	ActiveTab    string
	StartTime    time.Time
}

var (
	globalDesktopState *VirtualDesktopState
	globalStateOnce    sync.Once
)

// GetGlobalDesktopState returns the singleton desktop state for the running agent.
func GetGlobalDesktopState() *VirtualDesktopState {
	globalStateOnce.Do(func() {
		globalDesktopState = &VirtualDesktopState{
			CurX:      960,
			CurY:      540,
			ActiveTab: "CLI",
			StartTime: time.Now(),
			HistoryLines: []string{
				"Barahn Remote Support Daemon (Agent Live)",
				"Type 'help' to view available commands.",
			},
		}
	})
	return globalDesktopState
}

// UpdateCursor updates the normalized mouse position (0.0 - 1.0) into target pixel coordinates.
func (s *VirtualDesktopState) UpdateCursor(normX, normY float64, screenW, screenH int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	px := int(normX * float64(screenW))
	py := int(normY * float64(screenH))

	if px < 0 {
		px = 0
	} else if px >= screenW {
		px = screenW - 1
	}
	if py < 0 {
		py = 0
	} else if py >= screenH {
		py = screenH - 1
	}

	s.CurX = px
	s.CurY = py
}

// HandleClick registers a mouse click event.
func (s *VirtualDesktopState) HandleClick(normX, normY float64, button int, screenW, screenH int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	px := int(normX * float64(screenW))
	py := int(normY * float64(screenH))
	s.CurX = px
	s.CurY = py
	s.ClickX = px
	s.ClickY = py
	s.ClickButton = button
	s.ClickTime = time.Now()

	// Check if clicked dock at bottom
	dockW := 480
	dockH := 52
	dockX := (screenW - dockW) / 2
	dockY := screenH - 70

	if px >= dockX && px <= dockX+dockW && py >= dockY && py <= dockY+dockH {
		relX := px - dockX
		tabIdx := relX / 96
		tabs := []string{"CLI", "Files", "Chat", "WebRTC", "Tools"}
		if tabIdx >= 0 && tabIdx < len(tabs) {
			s.ActiveTab = tabs[tabIdx]
			s.HistoryLines = append(s.HistoryLines, fmt.Sprintf("[Dock] Switched to %s tab", tabs[tabIdx]))
			if len(s.HistoryLines) > 15 {
				s.HistoryLines = s.HistoryLines[1:]
			}
		}
	}
}

// HandleKey handles keyboard typing in the interactive terminal.
func (s *VirtualDesktopState) HandleKey(key, code string, osName, hostname string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch key {
	case "Enter":
		cmd := strings.TrimSpace(s.InputBuffer)
		s.HistoryLines = append(s.HistoryLines, fmt.Sprintf("barahn@%s:~$ %s", hostname, s.InputBuffer))
		s.InputBuffer = ""

		if cmd != "" {
			s.executeCommand(cmd, osName, hostname)
		}
		for len(s.HistoryLines) > 14 {
			s.HistoryLines = s.HistoryLines[1:]
		}

	case "Backspace":
		if len(s.InputBuffer) > 0 {
			s.InputBuffer = s.InputBuffer[:len(s.InputBuffer)-1]
		}

	case "Tab":
		if strings.HasPrefix(s.InputBuffer, "st") {
			s.InputBuffer = "status"
		} else if strings.HasPrefix(s.InputBuffer, "he") {
			s.InputBuffer = "help"
		} else if strings.HasPrefix(s.InputBuffer, "sy") {
			s.InputBuffer = "systemctl status barahn-agent"
		}

	default:
		if len(key) == 1 {
			ch := key[0]
			if _, ok := font5x7[ch]; ok {
				s.InputBuffer += key
			}
		}
	}
}

func (s *VirtualDesktopState) executeCommand(cmd, osName, hostname string) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return
	}
	base := strings.ToLower(parts[0])

	switch base {
	case "help", "?":
		s.HistoryLines = append(s.HistoryLines,
			"Available Commands:",
			"  status / systemctl status  - View live agent telemetry & uptime",
			"  ls / dir                   - List virtual filesystem contents",
			"  uname -a / ver             - Display OS & kernel information",
			"  ping <host>                - Network latency benchmark test",
			"  whoami                     - Print current active user",
			"  date                       - Print current UTC timestamp",
			"  clear / cls                - Clear terminal window",
			"  echo <text>                - Print text string to console",
		)

	case "clear", "cls":
		s.HistoryLines = []string{}

	case "status":
		uptime := time.Since(s.StartTime).Round(time.Second)
		s.HistoryLines = append(s.HistoryLines,
			fmt.Sprintf("● barahn-agent (PID %d) - Online (Uptime: %s)", 1042, uptime),
			"  Yamux Stream: WSS Multiplexed (Latency: ~0.15ms) [Active]",
			"  Heartbeat:    Woodstock Chirp Signal (every 15s) [OK]",
			"  Video Stream: WebRTC 1080p @ 30 FPS [Active]",
		)

	case "systemctl":
		s.HistoryLines = append(s.HistoryLines,
			"● barahn-agent.service - Barahn Remote Support Daemon",
			"     Loaded: loaded (/etc/systemd/system/barahn-agent.service)",
			"     Active: active (running) via Yamux Reverse WSS Tunnel",
			"     Memory: 24.8M | Threads: 8 | CPU: 0.2%",
		)

	case "ls", "dir":
		s.HistoryLines = append(s.HistoryLines,
			"drwxr-xr-x  bin/   certs/   data/   logs/   web/",
			"-rw-r--r--  agent.pem (Provisioned Credentials)",
			"-rw-r--r--  barahn.log (Telemetry Audit Log)",
			"-rwxr-xr-x  barahn (Unified CLI Daemon)",
		)

	case "uname", "ver":
		if osName == "windows" {
			s.HistoryLines = append(s.HistoryLines, "Microsoft Windows 11 Enterprise [Version 10.0.26100.1] (Wine 9.0 Virtualized)")
		} else {
			s.HistoryLines = append(s.HistoryLines, "Linux "+hostname+" 6.8.0-generic #45-Ubuntu SMP PREEMPT_DYNAMIC x86_64 GNU/Linux")
		}

	case "ping":
		s.HistoryLines = append(s.HistoryLines,
			"PING control-plane (127.0.0.1) 56(84) bytes of data.",
			"64 bytes from 127.0.0.1: icmp_seq=1 ttl=64 time=0.142 ms",
			"64 bytes from 127.0.0.1: icmp_seq=2 ttl=64 time=0.138 ms",
			"--- control-plane ping statistics --- 2 packets, 0% packet loss",
		)

	case "whoami":
		s.HistoryLines = append(s.HistoryLines, "barahn-operator (Admin Group: wheel)")

	case "date":
		s.HistoryLines = append(s.HistoryLines, time.Now().UTC().Format(time.RFC1123))

	case "echo":
		msg := strings.TrimPrefix(cmd, parts[0])
		s.HistoryLines = append(s.HistoryLines, strings.TrimSpace(msg))

	default:
		s.HistoryLines = append(s.HistoryLines, fmt.Sprintf("barahn: command not found: %s (type 'help')", cmd))
	}
}

// IsBlackFrame checks whether a buffer consists entirely of zero/black pixels (typical on XWayland root window).
func IsBlackFrame(buf []byte, w, h int) bool {
	if len(buf) < w*h*4 {
		return true
	}
	step := (w * h * 4) / 200
	if step < 4 {
		step = 4
	}
	for i := 0; i < len(buf); i += step {
		if buf[i] != 0 || buf[i+1] != 0 || buf[i+2] != 0 {
			return false
		}
	}
	return true
}

// RenderDesktop fills the RGBA buffer with a large, crisp, high-fidelity responsive workspace.
func RenderDesktop(dst []byte, w, h int, osName, hostname, agentID string, seq uint64, t time.Time, curX, curY int) {
	state := GetGlobalDesktopState()
	RenderDesktopState(dst, w, h, osName, hostname, agentID, seq, t, state)
}

// RenderDesktopState renders the desktop reflecting the current interactive state with large readable fonts.
func RenderDesktopState(dst []byte, w, h int, osName, hostname, agentID string, seq uint64, t time.Time, state *VirtualDesktopState) {
	if len(dst) < w*h*4 {
		return
	}

	state.mu.Lock()
	curX := state.CurX
	curY := state.CurY
	inputBuf := state.InputBuffer
	history := make([]string, len(state.HistoryLines))
	copy(history, state.HistoryLines)
	activeTab := state.ActiveTab
	clickTime := state.ClickTime
	clickX := state.ClickX
	clickY := state.ClickY
	state.mu.Unlock()

	// 1. Draw Modern Deep Blue/Navy Gradient Wallpaper (#0b0f19 to #030712)
	for y := 0; y < h; y++ {
		ratio := float32(y) / float32(h)
		r := byte(11 - int(8*ratio))
		g := byte(15 - int(8*ratio))
		b := byte(25 - int(7*ratio))

		rowOffset := y * w * 4
		for x := 0; x < w; x++ {
			idx := rowOffset + x*4
			// Subtle grid dots every 40px
			if (x%40 == 0 || y%40 == 0) && y > 44 && y < h-50 {
				dst[idx+0] = r + 6
				dst[idx+1] = g + 10
				dst[idx+2] = b + 16
			} else {
				dst[idx+0] = r
				dst[idx+1] = g
				dst[idx+2] = b
			}
			dst[idx+3] = 255
		}
	}

	// 2. Top Navigation Bar (height 44px)
	drawRect(dst, w, 0, 0, w, 44, color.RGBA{15, 23, 42, 255})
	drawHLine(dst, w, 0, w, 44, color.RGBA{51, 65, 85, 255})

	// Brand & Endpoint Identifier (Scale 2 for high clarity)
	badgeColor := color.RGBA{56, 189, 248, 255}
	if osName == "windows" {
		badgeColor = color.RGBA{96, 165, 250, 255}
	}
	drawText(dst, w, 20, 14, fmt.Sprintf("BARAHN CONTROL  |  %s (%s)", hostname, osName), badgeColor, 2)

	// Clock & Status Telemetry (Scale 2)
	timeStr := fmt.Sprintf("%02d:%02d:%02d UTC  [ONLINE 30 FPS]", t.Hour(), t.Minute(), t.Second())
	drawText(dst, w, w-420, 14, timeStr, color.RGBA{52, 211, 153, 255}, 2)

	// 3. Central Interactive Terminal Window (Large: 88% width, 76% height)
	winW := int(float64(w) * 0.88)
	winH := int(float64(h) * 0.74)
	winX := (w - winW) / 2
	winY := 56

	// Outer Window Shadow
	drawRect(dst, w, winX+8, winY+8, winW, winH, color.RGBA{2, 6, 23, 200})

	// Terminal Background
	termBg := color.RGBA{13, 17, 23, 255}
	drawRect(dst, w, winX, winY, winW, winH, termBg)
	drawRectOutline(dst, w, winX, winY, winW, winH, color.RGBA{51, 65, 85, 255})

	// Window Header Bar (height 40px)
	drawRect(dst, w, winX, winY, winW, 40, color.RGBA{22, 27, 34, 255})
	drawHLine(dst, w, winX, winX+winW, winY+40, color.RGBA{48, 54, 61, 255})

	// Mac/Linux Window Control Buttons (Red, Yellow, Green)
	drawCircle(dst, w, winX+22, winY+20, 7, color.RGBA{239, 68, 68, 255})
	drawCircle(dst, w, winX+44, winY+20, 7, color.RGBA{245, 158, 11, 255})
	drawCircle(dst, w, winX+66, winY+20, 7, color.RGBA{16, 185, 129, 255})

	titleHeader := fmt.Sprintf("Interactive Remote Shell — barahn@%s (ID: %s)", hostname, agentID)
	drawText(dst, w, winX+95, winY+12, titleHeader, color.RGBA{226, 232, 240, 255}, 2)

	// Terminal Content (Scale 2 for crisp readability!)
	termX := winX + 28
	termY := winY + 56

	// System Header Banner
	drawText(dst, w, termX, termY, "Barahn Unified Remote Support System [Active Session]", color.RGBA{56, 189, 248, 255}, 2)
	termY += 28

	displayDesc := "Virtual Xvfb (:99)"
	if strings.Contains(strings.ToLower(osName), "wayland") {
		displayDesc = "Weston Wayland (wayland-0)"
	} else if strings.Contains(strings.ToLower(osName), "windows") {
		displayDesc = "Win32 GDI (Primary)"
	}
	drawText(dst, w, termX, termY, fmt.Sprintf("OS: %s (amd64)  |  Display: %s  |  Frame: #%d", osName, displayDesc, seq), color.RGBA{148, 163, 184, 255}, 2)
	termY += 24
	drawHLine(dst, w, termX, winX+winW-28, termY, color.RGBA{30, 41, 59, 255})
	termY += 16

	// Render Command Output History
	for _, line := range history {
		if termY > winY+winH-80 {
			break
		}
		lineColor := color.RGBA{203, 213, 225, 255}
		if strings.HasPrefix(line, "barahn@") || strings.HasPrefix(line, "PS ") {
			lineColor = color.RGBA{56, 189, 248, 255}
		} else if strings.Contains(line, "Active:") || strings.Contains(line, "Online") {
			lineColor = color.RGBA{52, 211, 153, 255}
		} else if strings.Contains(line, "not found") || strings.Contains(line, "Error") {
			lineColor = color.RGBA{248, 113, 113, 255}
		}
		drawText(dst, w, termX, termY, line, lineColor, 2)
		termY += 24
	}

	// Render Interactive Command Prompt with Blinking Cursor
	promptPrefix := fmt.Sprintf("barahn@%s:~$ ", hostname)
	if osName == "windows" {
		promptPrefix = "PS C:\\Barahn\\Agent> "
	}
	drawText(dst, w, termX, termY, promptPrefix, color.RGBA{56, 189, 248, 255}, 2)

	promptLen := len(promptPrefix) * (5 + 1) * 2
	drawText(dst, w, termX+promptLen, termY, inputBuf, color.RGBA{248, 250, 252, 255}, 2)

	// Blinking Block Cursor █
	cursorOffset := promptLen + len(inputBuf)*(5+1)*2
	if (t.UnixNano()/500000000)%2 == 0 {
		drawRect(dst, w, termX+cursorOffset, termY-2, 12, 18, color.RGBA{56, 189, 248, 255})
	}

	// 4. Bottom Dock Menu (Interactive Tabs)
	dockW := 480
	dockH := 52
	dockX := (w - dockW) / 2
	dockY := h - 70

	drawRect(dst, w, dockX, dockY, dockW, dockH, color.RGBA{15, 23, 42, 240})
	drawRectOutline(dst, w, dockX, dockY, dockW, dockH, color.RGBA{51, 65, 85, 255})

	tabs := []string{"[CLI]", "[Files]", "[Chat]", "[WebRTC]", "[Tools]"}
	for i, tab := range tabs {
		tx := dockX + 12 + i*92
		tabName := strings.Trim(tab, "[]")
		tabBg := color.RGBA{30, 41, 59, 255}
		tabText := color.RGBA{148, 163, 184, 255}
		if tabName == activeTab {
			tabBg = color.RGBA{56, 189, 248, 60}
			tabText = color.RGBA{56, 189, 248, 255}
		}
		drawRect(dst, w, tx, dockY+8, 80, 36, tabBg)
		drawRectOutline(dst, w, tx, dockY+8, 80, 36, color.RGBA{71, 85, 105, 255})
		drawText(dst, w, tx+10, dockY+18, tab, tabText, 2)
	}

	// 5. Click Ripple Effect (Visual feedback when mouse is clicked!)
	if time.Since(clickTime) < 350*time.Millisecond {
		radius := int(float64(time.Since(clickTime).Milliseconds()) / 15.0)
		if radius < 25 {
			drawCircle(dst, w, clickX, clickY, radius, color.RGBA{56, 189, 248, 150})
		}
	}

	// 6. Draw High-Visibility Mouse Pointer Cursor
	drawCursor(dst, w, curX, curY)
}

func drawRect(dst []byte, stride, x, y, rw, rh int, c color.RGBA) {
	for dy := 0; dy < rh; dy++ {
		py := y + dy
		row := py * stride * 4
		for dx := 0; dx < rw; dx++ {
			px := x + dx
			idx := row + px*4
			if idx >= 0 && idx+3 < len(dst) {
				dst[idx+0] = c.R
				dst[idx+1] = c.G
				dst[idx+2] = c.B
				dst[idx+3] = c.A
			}
		}
	}
}

func drawRectOutline(dst []byte, stride, x, y, rw, rh int, c color.RGBA) {
	drawHLine(dst, stride, x, x+rw, y, c)
	drawHLine(dst, stride, x, x+rw, y+rh, c)
	drawVLine(dst, stride, x, y, y+rh, c)
	drawVLine(dst, stride, x+rw, y, y+rh, c)
}

func drawHLine(dst []byte, stride, x1, x2, y int, c color.RGBA) {
	row := y * stride * 4
	for x := x1; x <= x2; x++ {
		idx := row + x*4
		if idx >= 0 && idx+3 < len(dst) {
			dst[idx+0] = c.R
			dst[idx+1] = c.G
			dst[idx+2] = c.B
			dst[idx+3] = c.A
		}
	}
}

func drawVLine(dst []byte, stride, x, y1, y2 int, c color.RGBA) {
	for y := y1; y <= y2; y++ {
		idx := y*stride*4 + x*4
		if idx >= 0 && idx+3 < len(dst) {
			dst[idx+0] = c.R
			dst[idx+1] = c.G
			dst[idx+2] = c.B
			dst[idx+3] = c.A
		}
	}
}

func drawCircle(dst []byte, stride, cx, cy, r int, c color.RGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				px := cx + dx
				py := cy + dy
				idx := py*stride*4 + px*4
				if idx >= 0 && idx+3 < len(dst) {
					dst[idx+0] = c.R
					dst[idx+1] = c.G
					dst[idx+2] = c.B
					dst[idx+3] = c.A
				}
			}
		}
	}
}

func drawText(dst []byte, stride, startX, startY int, text string, c color.RGBA, scale int) {
	curX := startX
	for i := 0; i < len(text); i++ {
		ch := text[i]
		glyph, ok := font5x7[ch]
		if !ok {
			glyph = font5x7[' ']
		}

		for row := 0; row < 7; row++ {
			rowBits := glyph[row]
			for col := 0; col < 5; col++ {
				if (rowBits & (1 << (4 - col))) != 0 {
					for sx := 0; sx < scale; sx++ {
						for sy := 0; sy < scale; sy++ {
							px := curX + col*scale + sx
							py := startY + row*scale + sy
							idx := py*stride*4 + px*4
							if idx >= 0 && idx+3 < len(dst) {
								dst[idx+0] = c.R
								dst[idx+1] = c.G
								dst[idx+2] = c.B
								dst[idx+3] = c.A
							}
						}
					}
				}
			}
		}
		curX += (5 + 1) * scale
	}
}

func drawCursor(dst []byte, stride, x, y int) {
	// Crisp High-DPI Mouse Cursor Arrow (24x32)
	cursorMask := [20][15]byte{
		{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 0, 0, 0},
		{1, 2, 2, 2, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 1, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{1, 2, 1, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{1, 1, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0},
		{1, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0},
	}

	for cy := 0; cy < 20; cy++ {
		for cx := 0; cx < 15; cx++ {
			val := cursorMask[cy][cx]
			if val == 0 {
				continue
			}
			px := x + cx
			py := y + cy
			idx := py*stride*4 + px*4
			if idx >= 0 && idx+3 < len(dst) {
				if val == 1 {
					dst[idx+0] = 0
					dst[idx+1] = 0
					dst[idx+2] = 0
					dst[idx+3] = 255
				} else {
					dst[idx+0] = 255
					dst[idx+1] = 255
					dst[idx+2] = 255
					dst[idx+3] = 255
				}
			}
		}
	}
}

// GenerateTestDesktopImage returns a complete test desktop frame as an RGBA image.
func GenerateTestDesktopImage(w, h int, osName, hostname, agentID string, seq uint64, t time.Time, curX, curY int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	state := GetGlobalDesktopState()
	RenderDesktopState(img.Pix, w, h, osName, hostname, agentID, seq, t, state)
	return img
}
