// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package bark_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
)

func TestBark_EnvelopeEncoding(t *testing.T) {
	chat := &bark.ChatMessage{
		SessionID: "sess-123",
		Sender:    "Technician",
		Text:      "Hello from technician",
		Timestamp: time.Now().UTC(),
	}

	envBytes, err := bark.EncodeEnvelope(bark.TypeChatMessage, "sess-123", chat)
	if err != nil {
		t.Fatalf("EncodeEnvelope failed: %v", err)
	}

	var env bark.Envelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		t.Fatalf("Unmarshal Envelope failed: %v", err)
	}

	if env.Type != bark.TypeChatMessage || env.SessionID != "sess-123" {
		t.Fatalf("Envelope metadata mismatch: %+v", env)
	}

	var decodedChat bark.ChatMessage
	if err := json.Unmarshal(env.Payload, &decodedChat); err != nil {
		t.Fatalf("Unmarshal chat payload failed: %v", err)
	}

	if decodedChat.Text != "Hello from technician" || decodedChat.Sender != "Technician" {
		t.Fatalf("Decoded chat mismatch: %+v", decodedChat)
	}
}

func TestBark_ChirpConsentTypes(t *testing.T) {
	req := &bark.ConsentRequestPayload{
		SessionID:            "sess-chirp-1",
		Code:                 "847-291",
		TechnicianName:       "Lead Support",
		RequestedPermissions: []string{"screen_view", "remote_control"},
		Timestamp:            time.Now().UTC(),
	}

	envBytes, err := bark.EncodeEnvelope(bark.TypeConsentRequest, "sess-chirp-1", req)
	if err != nil {
		t.Fatalf("EncodeEnvelope failed for ConsentRequest: %v", err)
	}

	var env bark.Envelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		t.Fatalf("Unmarshal Envelope failed: %v", err)
	}
	if env.Type != bark.TypeConsentRequest {
		t.Fatalf("Expected type %s, got %s", bark.TypeConsentRequest, env.Type)
	}

	res := &bark.ConsentResponsePayload{
		SessionID: "sess-chirp-1",
		Code:      "847-291",
		Granted:   true,
		Timestamp: time.Now().UTC(),
	}
	resBytes, err := bark.EncodeEnvelope(bark.TypeConsentResponse, "sess-chirp-1", res)
	if err != nil {
		t.Fatalf("EncodeEnvelope failed for ConsentResponse: %v", err)
	}

	var resEnv bark.Envelope
	_ = json.Unmarshal(resBytes, &resEnv)
	if resEnv.Type != bark.TypeConsentResponse {
		t.Fatalf("Expected type %s, got %s", bark.TypeConsentResponse, resEnv.Type)
	}
}

func TestConsentResponse_LegacyAllOrNothing(t *testing.T) {
	requested := []string{"screen_view", "remote_control", "clipboard"}

	// Legacy payload without granted_permissions field: granted = true
	legacyTrueJSON := `{"session_id":"s1","code":"123","granted":true}`
	var resTrue bark.ConsentResponsePayload
	if err := json.Unmarshal([]byte(legacyTrueJSON), &resTrue); err != nil {
		t.Fatalf("Unmarshal legacyTrueJSON failed: %v", err)
	}
	permsTrue := resTrue.Permissions(requested)
	if len(permsTrue) != len(requested) {
		t.Fatalf("Expected %d permissions, got %v", len(requested), permsTrue)
	}
	for i, p := range requested {
		if permsTrue[i] != p {
			t.Fatalf("Expected permission %q at %d, got %q", p, i, permsTrue[i])
		}
	}

	// Legacy payload without granted_permissions field: granted = false
	legacyFalseJSON := `{"session_id":"s1","code":"123","granted":false}`
	var resFalse bark.ConsentResponsePayload
	if err := json.Unmarshal([]byte(legacyFalseJSON), &resFalse); err != nil {
		t.Fatalf("Unmarshal legacyFalseJSON failed: %v", err)
	}
	permsFalse := resFalse.Permissions(requested)
	if len(permsFalse) != 0 {
		t.Fatalf("Expected 0 permissions for granted=false, got %v", permsFalse)
	}
}

func TestConsentResponse_PartialGrant(t *testing.T) {
	requested := []string{"screen_view", "remote_control", "clipboard"}

	res := bark.ConsentResponsePayload{
		SessionID:          "s2",
		Code:               "456",
		Granted:            true,
		GrantedPermissions: []string{"screen_view", "clipboard"},
	}

	perms := res.Permissions(requested)
	expected := []string{"screen_view", "clipboard"}
	if len(perms) != len(expected) {
		t.Fatalf("Expected %d permissions, got %v", len(expected), perms)
	}
	for i, p := range expected {
		if perms[i] != p {
			t.Fatalf("Expected permission %q at %d, got %q", p, i, perms[i])
		}
	}
}

func TestConsentResponse_DropsUnrequestedAndDeduplicates(t *testing.T) {
	requested := []string{"screen_view", "clipboard"}

	res := bark.ConsentResponsePayload{
		SessionID:          "s3",
		Code:               "789",
		Granted:            true,
		GrantedPermissions: []string{"screen_view", "screen_view", "file_transfer", "arbitrary_privilege"},
	}

	perms := res.Permissions(requested)
	expected := []string{"screen_view"}
	if len(perms) != len(expected) {
		t.Fatalf("Expected %d permissions, got %v", len(expected), perms)
	}
	if perms[0] != "screen_view" {
		t.Fatalf("Expected %q, got %q", "screen_view", perms[0])
	}

	// Empty slice explicitly granted
	resEmpty := bark.ConsentResponsePayload{
		SessionID:          "s3-empty",
		Code:               "789",
		Granted:            true,
		GrantedPermissions: []string{},
	}
	permsEmpty := resEmpty.Permissions(requested)
	if len(permsEmpty) != 0 {
		t.Fatalf("Expected 0 permissions for empty GrantedPermissions, got %v", permsEmpty)
	}
}

func TestConsentResponse_RoundTripAndOmitempty(t *testing.T) {
	// Round trip with GrantedPermissions
	res := bark.ConsentResponsePayload{
		SessionID:          "s4",
		Code:               "101",
		Granted:            true,
		GrantedPermissions: []string{"screen_view"},
		Timestamp:          time.Now().UTC().Truncate(time.Millisecond),
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded bark.ConsentResponsePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(decoded.GrantedPermissions) != 1 || decoded.GrantedPermissions[0] != "screen_view" {
		t.Fatalf("Expected [screen_view], got %v", decoded.GrantedPermissions)
	}

	// When GrantedPermissions is nil, omitempty must omit it
	resNil := bark.ConsentResponsePayload{
		SessionID: "s5",
		Code:      "102",
		Granted:   false,
	}
	dataNil, err := json.Marshal(resNil)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if string(dataNil) != `{"session_id":"s5","code":"102","granted":false}` {
		var raw map[string]interface{}
		_ = json.Unmarshal(dataNil, &raw)
		if _, exists := raw["granted_permissions"]; exists {
			t.Fatalf("granted_permissions should be omitted when nil, got: %s", string(dataNil))
		}
	}
}

