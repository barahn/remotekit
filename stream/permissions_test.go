// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"slices"
	"sync"
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

// Without RequireScreenViewConsent the screen is streamed with or without
// screen_view, so viewers are told it is allowed rather than shown it as
// refused.
func TestAnUngatedScreenIsReportedAsAllowed(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.Grant(PermissionClipboard)
	if got, want := r.effectivePermissions(), []string{PermissionClipboard, PermissionScreenView}; !slices.Equal(got, want) {
		t.Fatalf("ungated: told %v, want %v", got, want)
	}
	r.RequireScreenViewConsent(true)
	if got, want := r.effectivePermissions(), []string{PermissionClipboard}; !slices.Equal(got, want) {
		t.Fatalf("gated: told %v, want %v", got, want)
	}
	if got := r.GrantedPermissions(); !slices.Equal(got, []string{PermissionClipboard}) {
		t.Fatalf("GrantedPermissions = %v, want only what was granted", got)
	}
}

// Changes racing each other are announced in order: once they stop, the last
// list a viewer was told is the state the runner is in.
func TestRacingChangesEndOnTheLatestList(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	r.RequireScreenViewConsent(true)
	r.Grant(PermissionScreenView)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	lists := make(chan []string, 1024)
	v.OnPermissions(func(granted []string) { lists <- granted })
	session(t, r, v)

	select {
	case <-lists:
	case <-time.After(20 * time.Second):
		t.Fatal("not told what is granted within 20s")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if (i+j)%2 == 0 {
					r.Grant(PermissionRemoteControl)
				} else {
					r.Revoke(PermissionRemoteControl)
				}
			}
		}(i)
	}
	wg.Wait()
	want := r.effectivePermissions()

	var last []string
	for {
		select {
		case l := <-lists:
			last = l
			continue
		case <-time.After(2 * time.Second):
		}
		break
	}
	if !slices.Equal(last, want) {
		t.Fatalf("last told %v, but the runner grants %v", last, want)
	}
}
