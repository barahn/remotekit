package bark_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mendsec/barahn/pkg/bark"
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

func TestBark_ScriptExecutionTypes(t *testing.T) {
	req := &bark.ScriptExecutionRequest{
		ExecutionID:    "exec-99",
		Interpreter:    "powershell",
		ScriptBody:     "Get-Process | Select-Object -First 5",
		TimeoutSeconds: 30,
	}

	envBytes, err := bark.EncodeEnvelope(bark.TypeScriptExecutionRequest, "agent-1", req)
	if err != nil {
		t.Fatalf("EncodeEnvelope failed: %v", err)
	}

	var env bark.Envelope
	_ = json.Unmarshal(envBytes, &env)
	if env.Type != bark.TypeScriptExecutionRequest {
		t.Fatalf("Expected type %s, got %s", bark.TypeScriptExecutionRequest, env.Type)
	}
}
