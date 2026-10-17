// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"testing"
	"time"
)

// With signed viewers required, an unsigned input message injected into the
// signalling socket by the relay must not reach the input injector, even
// when the person at the machine has consented to remote control.
func TestRequireSignedViewers_RefusesSocketInput(t *testing.T) {
	pub, _ := viewerKey(t)
	inj := &countingInjector{}
	r := &Runner{id: "agent-1", injector: inj}
	r.Grant(PermissionRemoteControl, PermissionFileTransfer)
	r.RequireSignedViewers(trustOnly(pub))
	h := startSignalHarness(t, r)

	h.send(map[string]interface{}{"type": "input", "payload": map[string]interface{}{"type": "mouse_move", "x": 0.5, "y": 0.5}})
	if m := h.expect("data_channel_required", 5*time.Second); m["message_type"] != "input" {
		t.Fatalf("refusal names %v, want input", m["message_type"])
	}
	h.send(map[string]interface{}{"type": "file_start", "transfer_id": "t1", "name": "a.txt", "size": 1})
	if m := h.expect("data_channel_required", 5*time.Second); m["message_type"] != "file_start" {
		t.Fatalf("refusal names %v, want file_start", m["message_type"])
	}
	if got := inj.moves.Load(); got != 0 {
		t.Fatalf("unsigned socket input was injected %d times with RequireSignedViewers on", got)
	}
}
