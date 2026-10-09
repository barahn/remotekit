// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"testing"

	"github.com/barahn/remotekit/input"
)

type recordingInjector struct {
	input.Injector
	moves int
}

func (i *recordingInjector) MoveMouse(x, y float64) error { i.moves++; return nil }

func TestInputDroppedWithoutRemoteControl(t *testing.T) {
	inj := &recordingInjector{}
	r := &Runner{injector: inj}
	ev := map[string]interface{}{"type": "mousemove", "x": 1.0, "y": 2.0}

	r.handleInputPayload(ev)
	if inj.moves != 0 {
		t.Fatalf("input injected without consent: %d moves", inj.moves)
	}

	r.Grant(PermissionClipboard) // another permission does not unlock input
	r.handleInputPayload(ev)
	if inj.moves != 0 {
		t.Fatalf("input injected with only clipboard granted")
	}

	r.Grant(PermissionRemoteControl)
	r.handleInputPayload(ev)
	if inj.moves != 1 {
		t.Fatalf("moves = %d after grant, want 1", inj.moves)
	}

	r.Revoke()
	r.handleInputPayload(ev)
	if inj.moves != 1 {
		t.Fatalf("input injected after revoke")
	}
}

func TestGrantRevoke(t *testing.T) {
	r := &Runner{}
	if r.Granted(PermissionFileTransfer) {
		t.Fatal("granted by default")
	}
	r.Grant(PermissionFileTransfer, PermissionClipboard)
	r.Revoke(PermissionClipboard)
	if !r.Granted(PermissionFileTransfer) || r.Granted(PermissionClipboard) {
		t.Fatal("selective revoke wrong")
	}
}

func TestStandingPermissionsSurviveClose(t *testing.T) {
	inj := &recordingInjector{}
	r := &Runner{injector: inj}
	ev := map[string]interface{}{"type": "mousemove", "x": 1.0, "y": 2.0}

	r.SetStandingPermissions(PermissionRemoteControl)
	r.handleInputPayload(ev)
	if inj.moves != 1 {
		t.Fatalf("moves = %d with a standing permission, want 1", inj.moves)
	}

	// A close revokes everything Grant gave, and only that.
	r.Grant(PermissionClipboard)
	r.Revoke()
	r.handleInputPayload(ev)
	if inj.moves != 2 {
		t.Fatalf("standing permission lost on close: moves = %d, want 2", inj.moves)
	}
	if r.Granted(PermissionClipboard) {
		t.Fatal("granted permission survived close")
	}

	r.Revoke(PermissionRemoteControl)
	if !r.Granted(PermissionRemoteControl) {
		t.Fatal("Revoke withdrew a standing permission")
	}

	r.SetStandingPermissions()
	r.handleInputPayload(ev)
	if inj.moves != 2 {
		t.Fatalf("input injected after standing permissions were cleared")
	}
}

func TestSetStandingPermissionsReplaces(t *testing.T) {
	r := &Runner{}
	r.SetStandingPermissions(PermissionRemoteControl, PermissionClipboard)
	r.SetStandingPermissions(PermissionFileTransfer)
	if r.Granted(PermissionRemoteControl) || r.Granted(PermissionClipboard) {
		t.Fatal("earlier standing permissions kept")
	}
	if !r.Granted(PermissionFileTransfer) {
		t.Fatal("new standing permission missing")
	}
}
