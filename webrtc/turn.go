// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G401,G505 -- TURN REST API specification mandates HMAC-SHA1 (draft-uberti-behave-turn-rest-00)
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// GenerateEphemeralTURNCredentials generates time-limited user/password pairs
// using a shared secret and expiry timestamp following the TURN REST API
// convention (RFC 5766 / draft-uberti-behave-turn-rest-00).
//
// The returned user is formatted as "<expiresAtUnix>:<username>" (or
// "<expiresAtUnix>" if username is empty). The returned pass is the base64-encoded
// HMAC-SHA1 signature of the user string under sharedSecret.
// expiresAt is the expiration timestamp in seconds since Unix epoch.
//
// Clients pass the returned user and pass into PeerConfig.TURNUsername and
// PeerConfig.TURNPassword alongside their TURN server URL in PeerConfig.ICEServers.
func GenerateEphemeralTURNCredentials(sharedSecret, username string, ttl time.Duration) (user, pass string, expiresAt int64) {
	expiresAt = time.Now().Add(ttl).Unix()
	if username != "" {
		user = fmt.Sprintf("%d:%s", expiresAt, username)
	} else {
		user = strconv.FormatInt(expiresAt, 10)
	}

	mac := hmac.New(sha1.New, []byte(sharedSecret)) // #nosec G401,G505 -- mandated by TURN REST API specification
	_, _ = mac.Write([]byte(user))
	pass = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return user, pass, expiresAt
}
