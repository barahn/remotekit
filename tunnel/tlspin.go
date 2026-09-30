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
	"sync"
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

// insecureWarning prints the unpinned skip-verify warning once per process.
// clientTLSConfig runs on every reconnect, so a flapping server would
// otherwise repeat it on every attempt and bury everything else in stderr.
var insecureWarning sync.Once

// clientTLSConfig returns the TLS configuration for talking to the control
// plane, or nil when the library default (full CA verification) is right.
//
// The pin stands in for the CA, so it applies only when CA verification is
// off. With insecureSkipVerify set and a pin recorded, the server must present
// the pinned key: that is what makes a self-signed development server safe to
// use after enrolment -- the agent trusts that key and no other, rather than
// anything at all.
//
// With CA verification on, the CA is the trust anchor and the pin is not
// checked. Enforcing it there as well would disconnect every agent whenever
// the server's key changes under a certificate the CA still vouches for --
// which ACME clients do on renewal by default, and which a load balancer in
// front of servers with different keys does on every connection. The only
// recovery would be re-enrolling each machine with a fresh pairing code.
//
// Without a pin and with insecureSkipVerify set, verification is off
// entirely. That is only ever true before enrolment has recorded a pin, and it
// is the trust-on-first-use window.
func clientTLSConfig(pin string, insecureSkipVerify bool) *tls.Config {
	if !insecureSkipVerify {
		return nil
	}
	if pin == "" {
		insecureWarning.Do(func() {
			fmt.Fprintln(os.Stderr, "[WARNING] TLS certificate verification is DISABLED and no server key is pinned. Connection is insecure!")
		})
		return &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- CLI opt-in flag for dev/test, before a pin exists
	}
	return &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- the pin check below binds the server's key in place of the CA
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
