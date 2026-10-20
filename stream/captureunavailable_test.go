// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/barahn/remotekit/permission"
	"github.com/barahn/remotekit/screen"
)

// refusingCapturer is a capturer whose Start fails, as macOS's does without
// Screen Recording.
type refusingCapturer struct {
	fakeCapturer
	err error
}

func (c *refusingCapturer) Start(context.Context) error { return c.err }

func captureUnavailable(t *testing.T, newCapturer func(screen.CaptureConfig) (screen.Capturer, error)) map[string]string {
	t.Helper()
	r := &Runner{id: "agent-1"}
	r.newCapturer = newCapturer
	data := firstReply(t, r, sessionStart, 5*time.Second)
	if got := replyType(t, data); got != "capture_unavailable" {
		t.Fatalf("reply type = %q, want capture_unavailable", got)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A machine that could capture but has not been allowed to says which
// permission is missing, so the viewer can tell the person at the other end
// what to grant rather than that capture is impossible. Still no frames: #157.
func TestCaptureUnavailableNamesTheMissingPermission(t *testing.T) {
	m := captureUnavailable(t, func(screen.CaptureConfig) (screen.Capturer, error) {
		return &refusingCapturer{err: permission.Missing(screen.ErrPermissionDenied, permission.ScreenRecording)}, nil
	})
	if m["permission"] != "screen_recording" {
		t.Errorf("permission = %q, want screen_recording", m["permission"])
	}
	if want := "the Screen Recording permission is not granted on this endpoint"; m["reason"] != want {
		t.Errorf("reason = %q, want %q", m["reason"], want)
	}
}

func TestCaptureUnavailableWithoutABackend(t *testing.T) {
	m := captureUnavailable(t, func(screen.CaptureConfig) (screen.Capturer, error) {
		return nil, fmt.Errorf("%w: not implemented", screen.ErrCaptureNotReady)
	})
	if _, ok := m["permission"]; ok {
		t.Errorf("a missing backend names permission %q", m["permission"])
	}
	if want := "no screen capture backend is available on this endpoint"; m["reason"] != want {
		t.Errorf("reason = %q, want %q", m["reason"], want)
	}
}
