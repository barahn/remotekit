// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

// ErrServerKeyMismatch means the server presented a key other than the one
// pinned at enrolment.
var ErrServerKeyMismatch = errors.New("tunnel: server key does not match the key pinned at enrolment")

// ServerKeyPin is the hex SHA-256 of a certificate's SubjectPublicKeyInfo.
//
// It pins the key rather than the certificate, so the server can renew its
// certificate without breaking every enrolled agent as long as it keeps the
// key.
func ServerKeyPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// clientTLSConfig returns the TLS configuration for talking to the control
// plane, or nil when the library default (full CA verification) is right.
//
// With a pin, the server must present the pinned key -- on top of normal CA
// verification, or instead of it when insecureSkipVerify is set. The second
// case is what makes a self-signed development server safe to use after
// enrolment: the agent trusts that key and no other, rather than anything at
// all.
//
// Without a pin and with insecureSkipVerify set, verification is off
// entirely. That is only ever true before enrolment has recorded a pin, and it
// is the trust-on-first-use window.
func clientTLSConfig(pin string, insecureSkipVerify bool) *tls.Config {
	if pin == "" {
		if !insecureSkipVerify {
			return nil
		}
		fmt.Fprintln(os.Stderr, "[WARNING] TLS certificate verification is DISABLED and no server key is pinned. Connection is insecure!")
		return &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- CLI opt-in flag for dev/test, before a pin exists
	}
	return &tls.Config{
		InsecureSkipVerify: insecureSkipVerify, // #nosec G402 -- the pin check below still binds the server's key
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrServerKeyMismatch
			}
			got := ServerKeyPin(cs.PeerCertificates[0])
			if subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
				return ErrServerKeyMismatch
			}
			return nil
		},
	}
}

// wsDialer returns the dialer for a control-plane WebSocket.
func wsDialer(pin string, insecureSkipVerify bool) *websocket.Dialer {
	cfg := clientTLSConfig(pin, insecureSkipVerify)
	if cfg == nil {
		return websocket.DefaultDialer
	}
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
		TLSClientConfig:  cfg,
	}
}
