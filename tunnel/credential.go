// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// HashCredential returns the hex-encoded SHA-256 of a credential: an agent
// token or a pairing code. Stores keep this, never the credential itself, and
// the tunnel looks credentials up by it.
//
// It is exported because the two sides have to agree. Whoever issues pairing
// codes stores HashCredential(code); HandlePairing looks the code up by the
// same value. A caller that hashes differently silently fails to match.
//
// Note what this is not: it is a plain digest, appropriate for a credential
// with full entropy, not a password hash. Do not feed it anything a person
// chose or could guess -- that wants argon2id with a salt.
func HashCredential(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

// newAgentToken mints a 256-bit credential from the system CSPRNG.
//
// The token is the only secret an agent presents to reconnect, so it must not
// be derivable from anything the agent publishes. It is returned to the agent
// once, at enrolment, and only its hash is stored.
func newAgentToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
