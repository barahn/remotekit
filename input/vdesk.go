// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package input

import "image"

// virtualDeskAbsolute maps a normalized (0.0-1.0) position within monitor —
// one monitor's rectangle in global desktop coordinates — to the 0-65535
// absolute coordinates Windows' SendInput reads when MOUSEEVENTF_VIRTUALDESK
// is set, for a virtual desktop spanning vdesk. Either rectangle may have a
// negative origin (a monitor left of or above the primary one).
//
// Windows turns an absolute coordinate n back into the pixel
// vdesk.Min + n*vdesk.Dx()/65536, so each pixel owns a band of n values.
// Aiming at the middle of the target pixel's band lands on exactly that
// pixel; scaling by Dx-1 and truncating, as a plain fraction does, drifts a
// pixel short across much of a monitor that is not at the desktop's left or
// top edge.
//
// It is pure so the mapping can be tested without a Windows desktop.
func virtualDeskAbsolute(monitor, vdesk image.Rectangle, x, y float64) (int32, int32) {
	return virtualDeskAxis(monitor.Min.X, monitor.Dx(), vdesk.Min.X, vdesk.Dx(), x),
		virtualDeskAxis(monitor.Min.Y, monitor.Dy(), vdesk.Min.Y, vdesk.Dy(), y)
}

func virtualDeskAxis(monMin, monSpan, vMin, vSpan int, f float64) int32 {
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	// The pixel on the monitor, then its offset into the virtual desktop.
	p := monMin + int(f*float64(monSpan-1)+0.5)
	off := int64(p - vMin)
	if off < 0 {
		off = 0
	} else if off > int64(vSpan-1) {
		off = int64(vSpan - 1)
	}
	n := (2*off + 1) * 65536 / (2 * int64(vSpan))
	if n > 65535 {
		n = 65535
	}
	return int32(n) // #nosec G115 -- n is clamped to [0, 65535]
}
