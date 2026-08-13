//go:build windows

package input

import (
	"image"
	"testing"
)

func TestWindowsInjector(t *testing.T) {
	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("NewInjector failed: %v", err)
	}
	defer inj.Close()

	inj.SetScreenBounds(image.Rect(0, 0, 1920, 1080))

	t.Run("NormalizeCoordinates", func(t *testing.T) {
		nx, ny := normalizeCoordinates(0.0, 0.0)
		if nx != 0 || ny != 0 {
			t.Errorf("expected (0,0), got (%d,%d)", nx, ny)
		}

		nx, ny = normalizeCoordinates(1.0, 1.0)
		if nx != 65535 || ny != 65535 {
			t.Errorf("expected (65535,65535), got (%d,%d)", nx, ny)
		}

		nx, ny = normalizeCoordinates(0.5, 0.5)
		if nx < 32700 || nx > 32800 || ny < 32700 || ny > 32800 {
			t.Errorf("expected ~32767, got (%d,%d)", nx, ny)
		}
	})

	t.Run("MapKeycode", func(t *testing.T) {
		vk, ext := mapKeycode("a", "KeyA")
		if vk != 0x41 || ext {
			t.Errorf("KeyA mapping failed, got vk=0x%X, ext=%v", vk, ext)
		}

		vk, ext = mapKeycode("Enter", "Enter")
		if vk != vkReturn || ext {
			t.Errorf("Enter mapping failed, got vk=0x%X, ext=%v", vk, ext)
		}

		vk, ext = mapKeycode("ArrowLeft", "ArrowLeft")
		if vk != vkLeft || !ext {
			t.Errorf("ArrowLeft mapping failed, got vk=0x%X, ext=%v", vk, ext)
		}

		vk, ext = mapKeycode("Meta", "MetaLeft")
		if vk != vkLWin || !ext {
			t.Errorf("MetaLeft mapping failed, got vk=0x%X, ext=%v", vk, ext)
		}
	})

	t.Run("MouseEvents", func(t *testing.T) {
		if err := inj.MoveMouse(0.5, 0.5); err != nil {
			t.Logf("MoveMouse info (may require active desktop session): %v", err)
		}
		if err := inj.MouseDown(ButtonLeft, 0.5, 0.5); err != nil {
			t.Logf("MouseDown info: %v", err)
		}
		if err := inj.MouseUp(ButtonLeft, 0.5, 0.5); err != nil {
			t.Logf("MouseUp info: %v", err)
		}
		if err := inj.Scroll(0, -1.0, 0.5, 0.5); err != nil {
			t.Logf("Scroll info: %v", err)
		}
	})

	t.Run("KeyboardEvents", func(t *testing.T) {
		if err := inj.KeyDown(KeyboardEvent{Key: "a", Code: "KeyA"}); err != nil {
			t.Logf("KeyDown info: %v", err)
		}
		if err := inj.KeyUp(KeyboardEvent{Key: "a", Code: "KeyA"}); err != nil {
			t.Logf("KeyUp info: %v", err)
		}
	})
}
