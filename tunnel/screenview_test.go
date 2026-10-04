// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"encoding/json"
	"testing"
	"time"
)

func replyType(t *testing.T, data []byte) string {
	t.Helper()
	if data == nil {
		t.Fatal("the agent sent no reply")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	typ, _ := m["type"].(string)
	return typ
}

var sessionStart = []byte(`{"type":"session_start","viewer_id":"viewer-a"}`)

// With screen-view consent required and not granted, the agent starts nothing
// and tells the viewer why.
func TestRequireScreenViewConsent_RefusesUntilGranted(t *testing.T) {
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	r.RequireScreenViewConsent(true)

	data := firstReply(t, r, sessionStart, 2*time.Second)
	if got := replyType(t, data); got != "consent_required" {
		t.Fatalf("reply type = %q, want consent_required", got)
	}
	var m map[string]string
	_ = json.Unmarshal(data, &m)
	if m["permission"] != PermissionScreenView {
		t.Fatalf("permission = %q, want %q", m["permission"], PermissionScreenView)
	}
}

// Once granted, the session starts: the reply is frames or the
// capture_unavailable notice, never a consent refusal.
func TestRequireScreenViewConsent_StartsOnceGranted(t *testing.T) {
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	r.RequireScreenViewConsent(true)
	r.Grant(PermissionScreenView)

	if got := replyType(t, firstReply(t, r, sessionStart, 5*time.Second)); got == "consent_required" {
		t.Fatal("screen_view was granted, yet the session was refused")
	}
}

// Off by default: no grant is needed and nothing is refused, as before.
func TestRequireScreenViewConsent_OffByDefault(t *testing.T) {
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	if got := replyType(t, firstReply(t, r, sessionStart, 5*time.Second)); got == "consent_required" {
		t.Fatal("without RequireScreenViewConsent the session must start without a grant")
	}
}

// Signed viewers and screen-view consent compose: a session needs both.
func TestRequireScreenViewConsent_AfterSignatureCheck(t *testing.T) {
	pub, _ := viewerKey(t)
	r := &AgentStreamRunner{creds: &AgentCredentials{AgentID: "agent-1", AgentToken: "tok"}}
	r.RequireSignedViewers(trustOnly(pub))
	r.RequireScreenViewConsent(true)

	// Unsigned: refused by the signature check before consent is consulted,
	// so the server learns nothing about the user's consent.
	if data := firstReply(t, r, sessionStart, 500*time.Millisecond); data != nil {
		t.Fatalf("unsigned session_start got a reply: %s", data)
	}
}
