package input

import (
	"image"
	"os"
	"testing"
)

func hasDisplay() bool {
	return os.Getenv("DISPLAY") != ""
}

func TestNewInjector(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 input injector test")
	}

	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("NewInjector failed: %v", err)
	}
	defer inj.Close()

	inj.SetScreenBounds(image.Rect(0, 0, 1920, 1080))
}

func TestInputEvents(t *testing.T) {
	if !hasDisplay() {
		t.Skip("DISPLAY not set — skipping X11 input injector test")
	}

	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("NewInjector failed: %v", err)
	}
	defer inj.Close()

	inj.SetScreenBounds(image.Rect(0, 0, 1920, 1080))

	// Test mouse move to center (0.5, 0.5)
	if err := inj.MoveMouse(0.5, 0.5); err != nil {
		t.Errorf("MoveMouse failed: %v", err)
	}

	// Test mouse scroll
	if err := inj.Scroll(0, 10, 0.5, 0.5); err != nil {
		t.Errorf("Scroll failed: %v", err)
	}

	// Test key press
	keyEvent := KeyboardEvent{
		Key:  "a",
		Code: "KeyA",
	}
	if err := inj.KeyDown(keyEvent); err != nil {
		t.Errorf("KeyDown failed: %v", err)
	}
	if err := inj.KeyUp(keyEvent); err != nil {
		t.Errorf("KeyUp failed: %v", err)
	}
}

func TestMapKeycode(t *testing.T) {
	tests := []struct {
		key  string
		code string
		want byte
	}{
		{"Enter", "Enter", 36},
		{"Escape", "Escape", 9},
		{"Backspace", "Backspace", 22},
		{"Tab", "Tab", 23},
		{"a", "KeyA", 38},
		{"b", "KeyB", 39},
		{"1", "Digit1", 10},
		{"0", "Digit0", 19},
	}

	for _, tt := range tests {
		got := mapKeycode(tt.key, tt.code)
		if got != tt.want {
			t.Errorf("mapKeycode(%q, %q) = %d, want %d", tt.key, tt.code, got, tt.want)
		}
	}
}
