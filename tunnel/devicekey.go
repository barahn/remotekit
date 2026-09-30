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
	"sync"
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
// An agent that sends a public_key which is not an Ed25519 key still enrols
// and connects with its token, but only until TunnelServer.TokenOnlyUntil.

const (
	// connectSignatureDomain separates connect proofs from anything else the
	// device key signs, such as signalling messages.
	connectSignatureDomain = "remotekit/connect/v1"

	// enrolSignatureDomain separates the enrolment proof from connect proofs.
	enrolSignatureDomain = "remotekit/enrol/v1"

	// ConnectSignatureSkew is how far a connect proof's timestamp may sit
	// from the server's clock in either direction.
	ConnectSignatureSkew = 60 * time.Second

	headerAgentTimestamp = "X-Barahn-Agent-Timestamp"
	headerAgentNonce     = "X-Barahn-Agent-Nonce"
	headerAgentSignature = "X-Barahn-Agent-Signature"
)

var (
	ErrDeviceKeyPermissions = errors.New("tunnel: device key file is readable by other users")
	ErrDeviceKeyMalformed   = errors.New("tunnel: device key file is not an Ed25519 private key")
	ErrConnectSigMissing    = errors.New("tunnel: connect proof missing")
	ErrConnectSigInvalid    = errors.New("tunnel: connect proof does not verify")
	ErrConnectSigStale      = errors.New("tunnel: connect proof timestamp outside the allowed skew")
	ErrConnectSigReplayed   = errors.New("tunnel: connect proof already used")
	ErrDeviceKeyRequired    = errors.New("tunnel: agent has no device key and token-only authentication has ended")
	ErrEnrolKeyMismatch     = errors.New("tunnel: pairing code was issued for a different device key")
	ErrEnrolProofInvalid    = errors.New("tunnel: enrolment proof missing or does not verify")
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

// Audiences a connect proof can be addressed to. The audience is signed, so
// a proof made for one endpoint does not verify at another: one captured on
// the tunnel cannot be replayed against the signalling socket, or the other
// way round. This is the role DPoP's htu claim plays (RFC 9449, and RFC 9700
// section 2.3 on audience-restricted credentials).
const (
	AudienceTunnel = "tunnel"
	AudienceSignal = "signal"
)

// ConnectProof is a proof of possession of the device key, as carried in the
// connect headers.
type ConnectProof struct {
	Timestamp string
	Nonce     string
	Signature string
}

// ConnectProofFromHeader reads the proof an agent sent with its connection.
func ConnectProofFromHeader(h http.Header) ConnectProof {
	return ConnectProof{
		Timestamp: h.Get(headerAgentTimestamp),
		Nonce:     h.Get(headerAgentNonce),
		Signature: h.Get(headerAgentSignature),
	}
}

func (p ConnectProof) setHeader(h http.Header) {
	h.Set(headerAgentTimestamp, p.Timestamp)
	h.Set(headerAgentNonce, p.Nonce)
	h.Set(headerAgentSignature, p.Signature)
}

// connectMessage is what a connect proof signs: the domain, the audience, the
// agent ID, the nonce and the timestamp. Binding the agent ID stops a proof
// being replayed for another agent, the audience for another endpoint; the
// timestamp bounds how long a captured one is useful, and the nonce lets a
// ReplayCache refuse it a second time within that window.
//
// None of the fields can contain the separator: the audience is one of the
// constants above, the agent ID is server-issued, and validNonce rejects
// anything outside base64url.
func connectMessage(audience, agentID, nonce string, unix int64) []byte {
	return []byte(connectSignatureDomain + "\x00" + audience + "\x00" + agentID + "\x00" + nonce + "\x00" + strconv.FormatInt(unix, 10))
}

// SignConnect returns a proof of possession of the device key for connecting
// as agentID to the endpoint named by audience. Every call draws a fresh
// nonce, so a proof is good for one connection.
func SignConnect(priv ed25519.PrivateKey, audience, agentID string, now time.Time) ConnectProof {
	ts := now.Unix()
	nonce := rand.Text()
	sig := ed25519.Sign(priv, connectMessage(audience, agentID, nonce, ts))
	return ConnectProof{
		Timestamp: strconv.FormatInt(ts, 10),
		Nonce:     nonce,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

// validNonce bounds the nonce to what a ReplayCache should hold: 16 to 64
// base64url characters. rand.Text's output (26 base32 characters) fits.
func validNonce(n string) bool {
	if len(n) < 16 || len(n) > 64 {
		return false
	}
	for _, c := range n {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// VerifyConnect checks a connect proof against the agent's registered device
// key, for the endpoint named by audience. It is exported so a product's own
// endpoints -- the signalling socket, for one, with AudienceSignal -- can hold
// agents to the same proof the tunnel does.
//
// A proof is valid for ConnectSignatureSkew either side of now, and once:
// seen records the nonce of every proof accepted and refuses it again for as
// long as the proof would otherwise stay fresh. seen must not be nil. Only
// proofs that verify and are fresh are recorded, so a stranger cannot fill it.
func VerifyConnect(pub ed25519.PublicKey, audience, agentID string, p ConnectProof, now time.Time, seen *ReplayCache) error {
	if p.Timestamp == "" || p.Nonce == "" || p.Signature == "" {
		return ErrConnectSigMissing
	}
	ts, err := strconv.ParseInt(p.Timestamp, 10, 64)
	if err != nil || !validNonce(p.Nonce) {
		return ErrConnectSigInvalid
	}
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrConnectSigInvalid
	}
	if !ed25519.Verify(pub, connectMessage(audience, agentID, p.Nonce, ts), sig) {
		return ErrConnectSigInvalid
	}
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(ConnectSignatureSkew/time.Second) {
		return ErrConnectSigStale
	}
	// Past ts+skew the timestamp check refuses the proof on its own, so the
	// nonce need only be remembered until then.
	if !seen.use(agentID+"\x00"+p.Nonce, time.Unix(ts, 0).Add(ConnectSignatureSkew), now) {
		return ErrConnectSigReplayed
	}
	return nil
}

// ReplayCache remembers the nonces of accepted connect proofs for as long as
// those proofs are fresh. The zero value is ready to use, and it is safe for
// concurrent use.
//
// It lives in one process's memory. Endpoints behind a load balancer that
// share one audience each keep their own, so a proof replayed within the skew
// window to a different replica is not caught -- it still needs the agent's
// token, which the proof does not reveal.
type ReplayCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time // key -> when the proof goes stale anyway
	lastSweep time.Time
}

// use records key until expires and reports whether it was new.
func (c *ReplayCache) use(key string, expires, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[string]time.Time)
	}
	// Sweep at most once per skew window, so entries live about twice the
	// window at worst and the cost is amortised across connections.
	if now.Sub(c.lastSweep) > ConnectSignatureSkew {
		for k, exp := range c.seen {
			if now.After(exp) {
				delete(c.seen, k)
			}
		}
		c.lastSweep = now
	}
	if exp, ok := c.seen[key]; ok && !now.After(exp) {
		return false
	}
	c.seen[key] = expires
	return true
}

// connectProofHeaders loads the credentials' device key and sets a connect
// proof for audience on h. Credentials without a device key are left alone:
// they authenticate with the token only, as before device keys existed.
//
// A configured key that cannot be loaded is an error, not a silent fall back
// to token-only -- a server that requires the proof would refuse the
// connection anyway, and one that does not would be accepting a weaker
// connection than the agent was set up for.
func connectProofHeaders(h http.Header, creds *AgentCredentials, audience string, now time.Time) error {
	if creds == nil || creds.DeviceKeyPath == "" {
		return nil
	}
	priv, err := LoadDeviceKey(creds.DeviceKeyPath)
	if err != nil {
		return fmt.Errorf("tunnel: loading device key: %w", err)
	}
	SignConnect(priv, audience, creds.AgentID, now).setHeader(h)
	return nil
}

// enrolMessage is what the enrolment proof signs: the pairing code, by its
// hash, and the public key being registered. Tying it to the code means the
// proof redeems that one single-use code and nothing else, so it needs no
// timestamp or nonce of its own.
func enrolMessage(pairingCode string, pub ed25519.PublicKey) []byte {
	return []byte(enrolSignatureDomain + "\x00" + HashCredential(pairingCode) + "\x00" + EncodeDevicePublicKey(pub))
}

// signEnrol proves, at enrolment, possession of the device key being
// registered.
func signEnrol(priv ed25519.PrivateKey, pairingCode string) string {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, enrolMessage(pairingCode, pub)))
}

func verifyEnrolProof(pub ed25519.PublicKey, pairingCode, proof string) error {
	sig, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, enrolMessage(pairingCode, pub), sig) {
		return ErrEnrolProofInvalid
	}
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
