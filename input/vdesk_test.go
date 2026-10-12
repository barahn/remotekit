// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package input

import (
	"image"
	"testing"
)

// windowsPixel is how SendInput turns a MOUSEEVENTF_VIRTUALDESK absolute
// coordinate back into a desktop pixel along one axis.
func windowsPixel(n int32, vMin, vSpan int) int {
	return vMin + int(int64(n)*int64(vSpan)/65536)
}

func TestVirtualDeskAbsolute(t *testing.T) {
	tests := []struct {
		name           string
		monitor, vdesk image.Rectangle
	}{
		{"single primary", image.Rect(0, 0, 1920, 1080), image.Rect(0, 0, 1920, 1080)},
		{"left of primary", image.Rect(-1280, 0, 0, 1024), image.Rect(-1280, 0, 1920, 1080)},
		{"left of and above primary", image.Rect(-1920, -1080, 0, 0), image.Rect(-1920, -1080, 1920, 1080)},
		{"primary with a monitor to its left", image.Rect(0, 0, 1920, 1080), image.Rect(-2560, -360, 1920, 1080)},
		{"right of primary", image.Rect(1920, 0, 4480, 1440), image.Rect(0, 0, 4480, 1440)},
		{"two 4K, secondary left", image.Rect(-3840, 0, 0, 2160), image.Rect(-3840, 0, 3840, 2160)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, v := tt.monitor, tt.vdesk
			// Every column and row of the monitor must land on itself.
			for p := 0; p < m.Dx(); p++ {
				f := float64(p) / float64(m.Dx()-1)
				nx, _ := virtualDeskAbsolute(m, v, f, 0)
				if got := windowsPixel(nx, v.Min.X, v.Dx()); got != m.Min.X+p {
					t.Fatalf("x=%v: lands on column %d, want %d", f, got, m.Min.X+p)
				}
			}
			for p := 0; p < m.Dy(); p++ {
				f := float64(p) / float64(m.Dy()-1)
				_, ny := virtualDeskAbsolute(m, v, 0, f)
				if got := windowsPixel(ny, v.Min.Y, v.Dy()); got != m.Min.Y+p {
					t.Fatalf("y=%v: lands on row %d, want %d", f, got, m.Min.Y+p)
				}
			}
			// Out-of-range input clamps to the monitor's edges, never a neighbour.
			nx, ny := virtualDeskAbsolute(m, v, -0.5, 1.5)
			if gx, gy := windowsPixel(nx, v.Min.X, v.Dx()), windowsPixel(ny, v.Min.Y, v.Dy()); gx != m.Min.X || gy != m.Max.Y-1 {
				t.Errorf("clamped to (%d,%d), want (%d,%d)", gx, gy, m.Min.X, m.Max.Y-1)
			}
		})
	}
}
