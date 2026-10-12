// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import "image"

// selectedDisplayRect returns the global rectangle of the monitor a capturer
// should grab: displays[idx] when idx resolves, otherwise the primary monitor
// (never the whole virtual desktop, which would leak the other monitors to the
// viewer), otherwise fallback. A degenerate rectangle keeps its origin but
// gets the fallback's size, so the result is always capturable.
//
// The result is in global desktop coordinates and is what Frame.Bounds
// carries; the frame's Image stays anchored at (0, 0).
func selectedDisplayRect(displays []Display, idx int, fallback image.Rectangle) image.Rectangle {
	r := fallback
	if idx >= 0 && idx < len(displays) {
		r = displays[idx].Bounds
	} else {
		for _, d := range displays {
			if d.Primary {
				r = d.Bounds
				break
			}
		}
	}
	if r.Dx() <= 0 || r.Dy() <= 0 {
		r = image.Rectangle{Min: r.Min, Max: r.Min.Add(fallback.Size())}
	}
	return r
}
