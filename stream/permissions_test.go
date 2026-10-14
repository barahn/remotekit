// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"slices"
	"testing"
	"time"

	"github.com/barahn/remotekit/screen"
)

func TestGrantedPermissionsListsGrantedAndStanding(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	if got := r.GrantedPermissions(); len(got) != 0 {
		t.Fatalf("a new runner grants %v", got)
	}
	r.Grant(PermissionScreenView, PermissionClipboard)
	r.SetStandingPermissions(PermissionScreenView, PermissionFileTransfer)
	want := []string{PermissionClipboard, PermissionFileTransfer, PermissionScreenView}
	if got := r.GrantedPermissions(); !slices.Equal(got, want) {
		t.Fatalf("granted = %v, want %v", got, want)
	}
	r.Revoke(PermissionClipboard)
	want = []string{PermissionFileTransfer, PermissionScreenView}
	if got := r.GrantedPermissions(); !slices.Equal(got, want) {
		t.Fatalf("after Revoke(clipboard), granted = %v, want %v", got, want)
	}
}

// A viewer learns what was granted when its control channel opens, and
// again when the person at the machine takes a permission away mid-session.
func TestViewerIsToldWhatIsGranted(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	r.RequireScreenViewConsent(true)
	r.Grant(PermissionScreenView, PermissionRemoteControl)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	lists := make(chan []string, 16)
	v.OnPermissions(func(granted []string) { lists <- granted })
	_, notices := session(t, r, v)

	waitFor := func(want []string) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case got := <-lists:
				if slices.Equal(got, want) {
					return
				}
			case n := <-notices:
				t.Fatalf("notice while waiting for %v: %+v", want, n)
			case <-deadline:
				t.Fatalf("not told %v within 20s", want)
			}
		}
	}
	waitFor([]string{PermissionRemoteControl, PermissionScreenView})
	r.Revoke(PermissionRemoteControl)
	waitFor([]string{PermissionScreenView})
}
