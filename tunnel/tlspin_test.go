// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTLSServerWithOwnKey is httptest.NewTLSServer with a freshly generated
// key. httptest's servers all share one built-in certificate, so two of them
// cannot stand in for a server and an impostor.
func newTLSServerWithOwnKey(t *testing.T) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "impostor"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	return srv
}

func dialWith(t *testing.T, srv *httptest.Server, cfg *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", strings.TrimPrefix(srv.URL, "https://"), cfg)
	if err == nil {
		_ = conn.Close()
	}
	return err
}

// With a pin, skip-verify stops meaning "anything goes": the pinned server is
// accepted and a different self-signed server is not.
func TestClientTLSConfig_PinBindsTheServerKey(t *testing.T) {
	good := httptest.NewTLSServer(http.NotFoundHandler())
	defer good.Close()
	impostor := newTLSServerWithOwnKey(t)
	defer impostor.Close()

	pin := ServerKeyPin(good.Certificate())
	if pin == ServerKeyPin(impostor.Certificate()) {
		t.Fatal("test servers unexpectedly share a key")
	}

	cfg := clientTLSConfig(pin, true)
	if err := dialWith(t, good, cfg); err != nil {
		t.Fatalf("pinned server refused: %v", err)
	}
	if err := dialWith(t, impostor, cfg); !errors.Is(err, ErrServerKeyMismatch) {
		t.Fatalf("impostor: want ErrServerKeyMismatch, got %v", err)
	}
}

// A pin never loosens CA verification: without skip-verify, the pinned key on
// an untrusted certificate is still refused, by the CA check.
func TestClientTLSConfig_PinDoesNotBypassCAVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()

	err := dialWith(t, srv, clientTLSConfig(ServerKeyPin(srv.Certificate()), false))
	if err == nil {
		t.Fatal("self-signed certificate accepted without skip-verify")
	}
	if errors.Is(err, ErrServerKeyMismatch) {
		t.Fatalf("expected a CA verification failure, got a pin mismatch: %v", err)
	}
}

// With CA verification on, the CA decides and the pin is not consulted. A
// server that renews onto a new key under a certificate the CA still trusts --
// the ACME default -- must not strand every enrolled agent.
func TestClientTLSConfig_CAValidRenewalOnNewKeyIsAccepted(t *testing.T) {
	old := httptest.NewTLSServer(http.NotFoundHandler())
	pin := ServerKeyPin(old.Certificate())
	old.Close()

	renewed := newTLSServerWithOwnKey(t)
	defer renewed.Close()
	if ServerKeyPin(renewed.Certificate()) == pin {
		t.Fatal("test servers unexpectedly share a key")
	}

	if cfg := clientTLSConfig(pin, false); cfg != nil {
		t.Fatalf("with CA verification on, the library default should apply, got %+v", cfg)
	}
	// The library default, with the renewed certificate's issuer trusted.
	pool := x509.NewCertPool()
	pool.AddCert(renewed.Certificate())
	if err := dialWith(t, renewed, &tls.Config{RootCAs: pool}); err != nil {
		t.Fatalf("CA-valid renewal on a new key refused: %v", err)
	}
}

func TestClientTLSConfig_DefaultIsLibraryVerification(t *testing.T) {
	if cfg := clientTLSConfig("", false); cfg != nil {
		t.Fatalf("no pin and no skip-verify should use the default config, got %+v", cfg)
	}
}

// Enrolment records the key the server presented, and the saved credentials
// carry it -- that is what every later connection is checked against.
func TestEnroll_PinsTheServerKey(t *testing.T) {
	store := NewMemStore()
	store.SeedPairingCode(HashCredential("PINCODE1"), PairingCode{
		ID:        "pc-pin",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})
	ts := NewTunnelServer(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/pair", ts.HandlePairing)
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	savePath := filepath.Join(dir, "agent.pem")
	creds, err := EnrollWithDeviceKey(srv.URL, "PINCODE1", "h", "linux", "amd64", filepath.Join(dir, "device.key"), savePath, true)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	want := ServerKeyPin(srv.Certificate())
	if creds.ServerKeyPin != want {
		t.Fatalf("returned pin = %q, want %q", creds.ServerKeyPin, want)
	}
	saved, err := LoadCredentials(savePath)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if saved.ServerKeyPin != want {
		t.Fatalf("saved pin = %q, want %q", saved.ServerKeyPin, want)
	}
}
