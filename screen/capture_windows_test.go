// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package screen

import (
	"context"
	"testing"
	"time"
)

func TestWindowsCapturer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetFPS = 10
	cap, err := NewCapturer(cfg)
	if err != nil {
		t.Fatalf("NewCapturer failed: %v", err)
	}
	defer cap.Stop()

	displays, err := cap.Displays()
	if err != nil {
		t.Fatalf("Displays() failed: %v", err)
	}
	if len(displays) == 0 {
		t.Fatal("expected at least 1 display detected")
	}

	t.Logf("Detected %d Windows displays: %+v", len(displays), displays)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := cap.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Try receiving frames
	select {
	case frame, ok := <-cap.Frames():
		if ok && frame != nil {
			t.Logf("Successfully captured frame: bounds=%v, seq=%d", frame.Bounds, frame.SequenceNum)
		}
	case <-time.After(500 * time.Millisecond):
		t.Log("No frame received within timeout (headless or display session not active)")
	}

	cap.Stop()
}
