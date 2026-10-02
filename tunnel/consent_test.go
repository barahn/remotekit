// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

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
	r := &AgentStreamRunner{injector: inj}
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
	r := &AgentStreamRunner{}
	if r.Granted(PermissionFileTransfer) {
		t.Fatal("granted by default")
	}
	r.Grant(PermissionFileTransfer, PermissionClipboard)
	r.Revoke(PermissionClipboard)
	if !r.Granted(PermissionFileTransfer) || r.Granted(PermissionClipboard) {
		t.Fatal("selective revoke wrong")
	}
}
