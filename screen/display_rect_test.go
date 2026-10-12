// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"image"
	"testing"
)

func TestSelectedDisplayRect(t *testing.T) {
	fallback := image.Rect(0, 0, 1920, 1080)
	// A secondary monitor left of and above the primary gives it a negative
	// origin, as EnumDisplayMonitors reports it on Windows.
	displays := []Display{
		{Index: 0, Bounds: image.Rect(-1280, -200, 0, 824)},
		{Index: 1, Bounds: image.Rect(0, 0, 1920, 1080), Primary: true},
		{Index: 2, Bounds: image.Rect(1920, 0, 4480, 1440)},
	}

	tests := []struct {
		name     string
		displays []Display
		idx      int
		want     image.Rectangle
	}{
		{"negative origin", displays, 0, image.Rect(-1280, -200, 0, 824)},
		{"primary", displays, 1, image.Rect(0, 0, 1920, 1080)},
		{"right of primary", displays, 2, image.Rect(1920, 0, 4480, 1440)},
		{"index past the end falls back to primary", displays, 3, image.Rect(0, 0, 1920, 1080)},
		{"negative index falls back to primary", displays, -1, image.Rect(0, 0, 1920, 1080)},
		{"no displays", nil, 0, fallback},
		{"no primary", []Display{{Bounds: image.Rect(10, 10, 20, 20)}}, 5, fallback},
		{"degenerate keeps origin", []Display{{Bounds: image.Rect(-1920, 0, -1920, 0)}}, 0, image.Rect(-1920, 0, 0, 1080)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectedDisplayRect(tt.displays, tt.idx, fallback); got != tt.want {
				t.Errorf("selectedDisplayRect(idx=%d) = %v, want %v", tt.idx, got, tt.want)
			}
		})
	}
}
