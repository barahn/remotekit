// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// A device key is the agent's own identity: an Ed25519 keypair generated on
// the host at enrolment, whose private half never leaves it. The server keeps
// only the public half, in AgentRegistration.PublicKey.
//
// It is what lets the server stop being the holder of the agent's secret.
// The agent token is still presented, but a server that knows the device key
// can also demand proof of the private key on every connection, and a copy of
// the token alone -- from a database dump, say -- no longer suffices.
//
// Enrolment stays compatible: an agent that sends a public_key which is not an
// Ed25519 key still enrols and still connects with its token, exactly as
// before. It simply has no device key to be held to.

const (
	// connectSignatureDomain separates connect proofs from anything else the
	// device key signs, such as signalling messages.
	connectSignatureDomain = "remotekit/connect/v1"

	// ConnectSignatureSkew is how far a connect proof's timestamp may sit
	// from the server's clock in either direction.
	ConnectSignatureSkew = 60 * time.Second

	headerAgentTimestamp = "X-Barahn-Agent-Timestamp"
	headerAgentSignature = "X-Barahn-Agent-Signature"
)

var (
	ErrDeviceKeyPermissions = errors.New("tunnel: device key file is readable by other users")
	ErrDeviceKeyMalformed   = errors.New("tunnel: device key file is not an Ed25519 private key")
	ErrConnectSigMissing    = errors.New("tunnel: connect proof missing")
	ErrConnectSigInvalid    = errors.New("tunnel: connect proof does not verify")
	ErrConnectSigStale      = errors.New("tunnel: connect proof timestamp outside the allowed skew")
)

// EncodeDevicePublicKey renders a device public key in the form enrolment
// sends and the store keeps: standard base64 of the 32 raw bytes.
func EncodeDevicePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ParseDevicePublicKey reads a stored public key. ok is false for anything
// that is not an Ed25519 key, which is how agents enrolled with a placeholder
// or a key of another type are recognised and left on token-only
// authentication.
func ParseDevicePublicKey(s string) (pub ed25519.PublicKey, ok bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(b), true
}

// LoadOrCreateDeviceKey returns the device key stored at path, generating and
// saving one if the file does not exist.
//
// The file is PKCS#8 PEM, written 0600 in a directory created 0700. An
// existing file that other users can read is refused rather than used: a
// device key anyone on the host can copy is no longer the device's.
func LoadOrCreateDeviceKey(path string) (ed25519.PrivateKey, error) {
	clean := filepath.Clean(path)
	data, err := os.ReadFile(clean) // #nosec G304 -- the caller chooses where its own key lives
	if errors.Is(err, os.ErrNotExist) {
		return createDeviceKey(clean)
	}
	if err != nil {
		return nil, err
	}
	if err := checkDeviceKeyPermissions(clean); err != nil {
		return nil, err
	}
	return parseDeviceKey(data)
}

func createDeviceKey(path string) (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tunnel: generating device key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	// O_EXCL: two processes enrolling at once must not overwrite each
	// other's key after one of them has already sent its public half.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- see LoadOrCreateDeviceKey
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(block); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return priv, nil
}

func parseDeviceKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, ErrDeviceKeyMalformed
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrDeviceKeyMalformed
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, ErrDeviceKeyMalformed
	}
	return priv, nil
}

// checkDeviceKeyPermissions refuses a key file group or others can read. On
// Windows the mode bits do not describe the ACL, so the check is skipped
// there rather than giving a false answer either way.
func checkDeviceKeyPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s is %v", ErrDeviceKeyPermissions, path, info.Mode().Perm())
	}
	return nil
}

// connectMessage is what a connect proof signs: the domain, the agent ID and
// the timestamp. Binding the agent ID stops a proof being replayed for
// another agent; the timestamp bounds how long a captured one is useful.
func connectMessage(agentID string, unix int64) []byte {
	return []byte(connectSignatureDomain + "\x00" + agentID + "\x00" + strconv.FormatInt(unix, 10))
}

// SignConnect returns the timestamp and signature headers that prove
// possession of the device key when connecting as agentID.
func SignConnect(priv ed25519.PrivateKey, agentID string, now time.Time) (timestamp, signature string) {
	ts := now.Unix()
	sig := ed25519.Sign(priv, connectMessage(agentID, ts))
	return strconv.FormatInt(ts, 10), base64.StdEncoding.EncodeToString(sig)
}

// VerifyConnect checks a connect proof against the agent's registered device
// key. It is exported so a product's own endpoints -- the signalling socket,
// for one -- can hold agents to the same proof the tunnel does.
//
// A proof is valid for ConnectSignatureSkew either side of now. Within that
// window a captured proof could be replayed, but only together with the
// agent's token, which it does not reveal; the proof's job is to make the
// token alone insufficient, not to replace it.
func VerifyConnect(pub ed25519.PublicKey, agentID, timestamp, signature string, now time.Time) error {
	if timestamp == "" || signature == "" {
		return ErrConnectSigMissing
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrConnectSigInvalid
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrConnectSigInvalid
	}
	if !ed25519.Verify(pub, connectMessage(agentID, ts), sig) {
		return ErrConnectSigInvalid
	}
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(ConnectSignatureSkew/time.Second) {
		return ErrConnectSigStale
	}
	return nil
}

// connectProofHeaders loads the credentials' device key and sets the connect
// proof on h. Credentials without a device key are left alone: they
// authenticate with the token only, as before device keys existed.
//
// A configured key that cannot be loaded is an error, not a silent fall back
// to token-only -- a server that requires the proof would refuse the
// connection anyway, and one that does not would be accepting a weaker
// connection than the agent was set up for.
func connectProofHeaders(h http.Header, creds *AgentCredentials, now time.Time) error {
	if creds == nil || creds.DeviceKeyPath == "" {
		return nil
	}
	priv, err := LoadDeviceKey(creds.DeviceKeyPath)
	if err != nil {
		return fmt.Errorf("tunnel: loading device key: %w", err)
	}
	ts, sig := SignConnect(priv, creds.AgentID, now)
	h.Set(headerAgentTimestamp, ts)
	h.Set(headerAgentSignature, sig)
	return nil
}

// LoadDeviceKey loads a device key that must already exist.
// Connecting is not the moment to mint a new identity: a key generated here
// would not match the one the server registered.
func LoadDeviceKey(path string) (ed25519.PrivateKey, error) {
	clean := filepath.Clean(path)
	data, err := os.ReadFile(clean) // #nosec G304 -- the credentials name their own key file
	if err != nil {
		return nil, err
	}
	if err := checkDeviceKeyPermissions(clean); err != nil {
		return nil, err
	}
	return parseDeviceKey(data)
}
