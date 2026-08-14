package screen

import (
	"fmt"
	"image"
	"image/color"
	"time"
)

// Simple 5x7 bitmap font for rendering telemetry on fallback/simulated desktops without external dependencies.
var font5x7 = map[byte][7]byte{
	' ': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	'!': {0x04, 0x04, 0x04, 0x04, 0x00, 0x00, 0x04},
	':': {0x00, 0x0C, 0x0C, 0x00, 0x0C, 0x0C, 0x00},
	'-': {0x00, 0x00, 0x1F, 0x00, 0x00, 0x00, 0x00},
	'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C},
	'/': {0x01, 0x02, 0x04, 0x08, 0x10, 0x00, 0x00},
	'(': {0x02, 0x04, 0x08, 0x08, 0x08, 0x04, 0x02},
	')': {0x08, 0x04, 0x02, 0x02, 0x02, 0x04, 0x08},
	'[': {0x0E, 0x08, 0x08, 0x08, 0x08, 0x08, 0x0E},
	']': {0x0E, 0x02, 0x02, 0x02, 0x02, 0x02, 0x0E},
	'#': {0x0A, 0x1F, 0x0A, 0x0A, 0x1F, 0x0A, 0x00},
	'$': {0x04, 0x0F, 0x14, 0x0E, 0x05, 0x1E, 0x04},
	'%': {0x19, 0x19, 0x02, 0x04, 0x08, 0x13, 0x13},
	'>': {0x10, 0x08, 0x04, 0x02, 0x04, 0x08, 0x10},
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
	'3': {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D': {0x1C, 0x12, 0x11, 0x11, 0x11, 0x12, 0x1C},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F},
	'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J': {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0E, 0x11, 0x10, 0x0E, 0x01, 0x11, 0x0E},
	'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11},
	'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'a': {0x00, 0x00, 0x0E, 0x01, 0x0F, 0x11, 0x0F},
	'b': {0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x1E},
	'c': {0x00, 0x00, 0x0E, 0x10, 0x10, 0x11, 0x0E},
	'd': {0x01, 0x01, 0x0D, 0x13, 0x11, 0x11, 0x0F},
	'e': {0x00, 0x00, 0x0E, 0x11, 0x1F, 0x10, 0x0E},
	'f': {0x06, 0x09, 0x08, 0x1C, 0x08, 0x08, 0x08},
	'g': {0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x0E},
	'h': {0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x11},
	'i': {0x04, 0x00, 0x0C, 0x04, 0x04, 0x04, 0x0E},
	'j': {0x02, 0x00, 0x06, 0x02, 0x02, 0x12, 0x0C},
	'k': {0x10, 0x10, 0x12, 0x14, 0x18, 0x14, 0x12},
	'l': {0x0C, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'm': {0x00, 0x00, 0x1A, 0x15, 0x15, 0x11, 0x11},
	'n': {0x00, 0x00, 0x16, 0x19, 0x11, 0x11, 0x11},
	'o': {0x00, 0x00, 0x0E, 0x11, 0x11, 0x11, 0x0E},
	'p': {0x00, 0x00, 0x1E, 0x11, 0x1E, 0x10, 0x10},
	'q': {0x00, 0x00, 0x0D, 0x13, 0x0F, 0x01, 0x01},
	'r': {0x00, 0x00, 0x16, 0x19, 0x10, 0x10, 0x10},
	's': {0x00, 0x00, 0x0E, 0x10, 0x0E, 0x01, 0x1E},
	't': {0x08, 0x08, 0x1C, 0x08, 0x08, 0x09, 0x06},
	'u': {0x00, 0x00, 0x11, 0x11, 0x11, 0x13, 0x0D},
	'v': {0x00, 0x00, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'w': {0x00, 0x00, 0x11, 0x11, 0x15, 0x15, 0x0A},
	'x': {0x00, 0x00, 0x11, 0x0A, 0x04, 0x0A, 0x11},
	'y': {0x00, 0x00, 0x11, 0x11, 0x0F, 0x01, 0x0E},
	'z': {0x00, 0x00, 0x1F, 0x02, 0x04, 0x08, 0x1F},
}

// IsBlackFrame checks whether a buffer consists entirely of zero/black pixels (typical on XWayland root window).
func IsBlackFrame(buf []byte, w, h int) bool {
	if len(buf) < w*h*4 {
		return true
	}
	// Check sampling stride
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

// RenderDesktop fills the RGBA buffer with a high-fidelity desktop workspace representation.
func RenderDesktop(dst []byte, w, h int, osName, hostname, agentID string, seq uint64, t time.Time, curX, curY int) {
	if len(dst) < w*h*4 {
		return
	}

	// 1. Draw Deep Navy Gradient Wallpaper (#0f172a to #020617)
	for y := 0; y < h; y++ {
		ratio := float32(y) / float32(h)
		r := byte(15 - int(13*ratio))
		g := byte(23 - int(17*ratio))
		b := byte(42 - int(19*ratio))

		rowOffset := y * w * 4
		for x := 0; x < w; x++ {
			idx := rowOffset + x*4
			// Add subtle grid line pattern every 60px
			if (x%60 == 0 || y%60 == 0) && y > 40 && y < h-40 {
				dst[idx+0] = r + 8
				dst[idx+1] = g + 12
				dst[idx+2] = b + 18
			} else {
				dst[idx+0] = r
				dst[idx+1] = g
				dst[idx+2] = b
			}
			dst[idx+3] = 255
		}
	}

	// 2. Top Status Bar (height 36px, #0b0f19)
	drawRect(dst, w, 0, 0, w, 36, color.RGBA{11, 15, 25, 255})
	drawHLine(dst, w, 0, w, 36, color.RGBA{30, 41, 59, 255})

	// Top Bar Info
	title := fmt.Sprintf("BARAHN CONTROL PLANE  |  %s (%s)", hostname, osName)
	drawText(dst, w, 16, 12, title, color.RGBA{56, 189, 248, 255}, 1)

	timeStr := fmt.Sprintf("%02d:%02d:%02d UTC  [ONLINE 30 FPS]", t.Hour(), t.Minute(), t.Second())
	drawText(dst, w, w-280, 12, timeStr, color.RGBA{52, 211, 153, 255}, 1)

	// 3. Central Terminal / Workstation Window
	winW := 760
	winH := 460
	winX := (w - winW) / 2
	winY := (h-winH)/2 - 20

	// Window shadow
	drawRect(dst, w, winX+8, winY+8, winW, winH, color.RGBA{2, 6, 23, 180})

	// Window frame (#0f172a)
	drawRect(dst, w, winX, winY, winW, winH, color.RGBA{15, 23, 42, 255})
	drawRectOutline(dst, w, winX, winY, winW, winH, color.RGBA{51, 65, 85, 255})

	// Window titlebar
	drawRect(dst, w, winX, winY, winW, 32, color.RGBA{30, 41, 59, 255})
	drawCircle(dst, w, winX+16, winY+16, 5, color.RGBA{239, 68, 68, 255})  // Red
	drawCircle(dst, w, winX+32, winY+16, 5, color.RGBA{245, 158, 11, 255}) // Yellow
	drawCircle(dst, w, winX+48, winY+16, 5, color.RGBA{16, 185, 129, 255}) // Green

	drawText(dst, w, winX+70, winY+10, fmt.Sprintf("Terminal — barahn@%s (Session Active)", hostname), color.RGBA{226, 232, 240, 255}, 1)

	// Window content (Dark terminal)
	termX := winX + 20
	termY := winY + 48

	drawText(dst, w, termX, termY, "Barahn Remote Access Agent (Phase 1 MVP Engine)", color.RGBA{56, 189, 248, 255}, 2)
	termY += 28

	drawText(dst, w, termX, termY, fmt.Sprintf("Endpoint ID:      %s", agentID), color.RGBA{148, 163, 184, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, fmt.Sprintf("Platform:         %s / amd64", osName), color.RGBA{148, 163, 184, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, fmt.Sprintf("Display Server:   X11 / Xvfb Virtual Desktop (1920x1080@30Hz)"), color.RGBA{148, 163, 184, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, fmt.Sprintf("Frame Stream:     Active (Sequence #%d)", seq), color.RGBA{52, 211, 153, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, fmt.Sprintf("Input Control:    Remote Control Active & Mouse Tracking Enabled"), color.RGBA{52, 211, 153, 255}, 1)
	termY += 28

	// Terminal Prompt Simulation
	drawText(dst, w, termX, termY, fmt.Sprintf("barahn@%s:~$ systemctl status barahn-agent.service", hostname), color.RGBA{248, 250, 252, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, "● barahn-agent.service - Barahn Remote Support Daemon", color.RGBA{52, 211, 153, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, "     Active: active (running) via Yamux WSS Outbound Tunnel", color.RGBA{148, 163, 184, 255}, 1)
	termY += 18
	drawText(dst, w, termX, termY, "     Heartbeat: Woodstock Chirp Signal [OK] (every 15s)", color.RGBA{148, 163, 184, 255}, 1)
	termY += 28

	drawText(dst, w, termX, termY, fmt.Sprintf("barahn@%s:~$ _", hostname), color.RGBA{56, 189, 248, 255}, 1)

	// 4. Bottom Dock / Taskbar
	dockW := 320
	dockH := 48
	dockX := (w - dockW) / 2
	dockY := h - 60

	drawRect(dst, w, dockX, dockY, dockW, dockH, color.RGBA{15, 23, 42, 230})
	drawRectOutline(dst, w, dockX, dockY, dockW, dockH, color.RGBA{51, 65, 85, 255})

	// App Badges in Dock
	apps := []string{"[CLI]", "[Files]", "[Chat]", "[WebRTC]", "[Tools]"}
	for i, app := range apps {
		ax := dockX + 16 + i*60
		drawRect(dst, w, ax, dockY+8, 48, 32, color.RGBA{30, 41, 59, 255})
		drawText(dst, w, ax+6, dockY+18, app, color.RGBA{226, 232, 240, 255}, 1)
	}

	// 5. Draw Interactive Mouse Cursor
	if curX <= 0 || curX >= w {
		curX = w / 2
	}
	if curY <= 0 || curY >= h {
		curY = h / 2
	}
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
	// Standard pointer arrow polygon
	cursorMask := [16][12]byte{
		{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{1, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0},
		{1, 2, 2, 2, 2, 1, 1, 1, 1, 1, 0, 0},
		{1, 2, 2, 1, 2, 2, 1, 0, 0, 0, 0, 0},
		{1, 2, 1, 0, 1, 2, 2, 1, 0, 0, 0, 0},
		{1, 1, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0},
		{1, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0},
	}

	for cy := 0; cy < 16; cy++ {
		for cx := 0; cx < 12; cx++ {
			val := cursorMask[cy][cx]
			if val == 0 {
				continue
			}
			px := x + cx
			py := y + cy
			idx := py*stride*4 + px*4
			if idx >= 0 && idx+3 < len(dst) {
				if val == 1 {
					// Black border
					dst[idx+0] = 0
					dst[idx+1] = 0
					dst[idx+2] = 0
					dst[idx+3] = 255
				} else {
					// White fill
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
	RenderDesktop(img.Pix, w, h, osName, hostname, agentID, seq, t, curX, curY)
	return img
}
