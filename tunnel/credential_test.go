// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHashCredentialActuallyHashes pins the property the previous code did not
// have. It used sha256.New().Sum(data), which appends the digest to data
// instead of hashing it, so the stored "hash" contained the credential in
// plaintext and ended in the SHA-256 of the empty string.
func TestHashCredentialActuallyHashes(t *testing.T) {
	const secret = "pairing-code-9F3K2"

	got := HashCredential(secret)

	if strings.Contains(got, hex.EncodeToString([]byte(secret))) {
		t.Errorf("credential recoverable from its hash: %s", got)
	}
	if strings.HasSuffix(got, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") {
		t.Errorf("hash ends in sha256(\"\"), so nothing was hashed: %s", got)
	}
	if len(got) != 64 {
		t.Errorf("hash is %d hex chars, want 64", len(got))
	}
	if HashCredential(secret) != got {
		t.Error("HashCredential is not deterministic")
	}
	if HashCredential(secret+"x") == got {
		t.Error("distinct credentials hash alike")
	}
}

// TestAgentTokenIsNotDerivable is the regression test for the actual hole: the
// reconnection token used to be a function of the agent id and public key,
// both of which the server itself publishes. Anyone holding them could mint it.
func TestAgentTokenIsNotDerivable(t *testing.T) {
	const code = "code-for-enrolment"
	store := NewMemStore()
	store.SeedPairingCode(HashCredential(code), PairingCode{
		ID: "pc-1", ExpiresAt: time.Now().Add(time.Hour),
	})

	ts := NewTunnelServer(store)
	srv := httptest.NewServer(http.HandlerFunc(ts.HandlePairing))
	defer srv.Close()

	body := `{"pairing_code":"` + code + `","hostname":"h","os":"linux","arch":"amd64","public_key":"PUBKEY"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var enrolled struct {
		AgentID    string `json:"agent_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enrolled); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if enrolled.AgentToken == "" {
		t.Fatal("no token issued")
	}

	// The old derivation, which must no longer produce anything usable.
	legacy := hex.EncodeToString([]byte(enrolled.AgentID + ":PUBKEY"))
	if enrolled.AgentToken == legacy || strings.HasPrefix(enrolled.AgentToken, legacy) {
		t.Error("token is still derived from the agent id and public key")
	}
	if strings.Contains(enrolled.AgentToken, hex.EncodeToString([]byte("PUBKEY"))) {
		t.Error("public key is recoverable from the token")
	}

	// What is stored is the hash, not the token.
	agent, err := store.GetAgentByID(context.Background(), enrolled.AgentID)
	if err != nil {
		t.Fatalf("GetAgentByID: %v", err)
	}
	if agent.TokenHash != HashCredential(enrolled.AgentToken) {
		t.Error("stored hash does not match the issued token")
	}
	if agent.TokenHash == enrolled.AgentToken {
		t.Error("the token itself was stored")
	}
}

// TestNewAgentTokenIsUnique guards against a constant or a low-entropy source.
func TestNewAgentTokenIsUnique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		tok, err := newAgentToken()
		if err != nil {
			t.Fatalf("newAgentToken: %v", err)
		}
		if len(tok) != 64 {
			t.Fatalf("token is %d hex chars, want 64 (256 bits)", len(tok))
		}
		if seen[tok] {
			t.Fatal("newAgentToken repeated a value")
		}
		seen[tok] = true
	}
}
