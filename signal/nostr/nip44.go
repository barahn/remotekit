// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"

	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/hkdf"
)

// NIP-44 version 2: ECDH on secp256k1, HKDF-SHA256, ChaCha20, HMAC-SHA256,
// with the plaintext padded so its length leaks only coarsely.
const (
	nip44Version      = 2
	nip44MinPlaintext = 1
	nip44MaxPlaintext = 65535
)

var (
	// ErrMessageTooLarge is returned for a message that does not fit a NIP-44
	// payload once wrapped.
	ErrMessageTooLarge = errors.New("nostr: message too large for a NIP-44 payload")

	errNIP44Payload = errors.New("nostr: malformed NIP-44 payload")
	errNIP44MAC     = errors.New("nostr: NIP-44 payload failed authentication")
	errNIP44Padding = errors.New("nostr: bad NIP-44 padding")
)

// conversationKey derives the NIP-44 conversation key shared by sec and pub:
// HKDF-Extract with salt "nip44-v2" over the x coordinate of their ECDH point.
// It is the same in both directions.
func conversationKey(sec *SecretKey, pub *btcec.PublicKey) [32]byte {
	shared := btcec.GenerateSharedSecret(sec.priv, pub)
	var k [32]byte
	copy(k[:], hkdf.Extract(sha256.New, shared, []byte("nip44-v2")))
	return k
}

// messageKeys expands a conversation key and a per-message nonce into the
// ChaCha20 key and nonce and the HMAC key.
func messageKeys(conv [32]byte, nonce []byte) (chachaKey, chachaNonce, hmacKey []byte, err error) {
	out := make([]byte, 76)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, conv[:], nonce), out); err != nil {
		return nil, nil, nil, err
	}
	return out[0:32], out[32:44], out[44:76], nil
}

// paddedLen is NIP-44's calc_padded_len: 32 bytes minimum, then chunks of
// 32 bytes up to 256, and of an eighth of the next power of two above that.
func paddedLen(n int) int {
	if n <= 32 {
		return 32
	}
	nextPower := 1
	for nextPower < n {
		nextPower <<= 1
	}
	chunk := 32
	if nextPower > 256 {
		chunk = nextPower / 8
	}
	return chunk * ((n-1)/chunk + 1)
}

func pad(plaintext []byte) ([]byte, error) {
	n := len(plaintext)
	if n < nip44MinPlaintext || n > nip44MaxPlaintext {
		return nil, ErrMessageTooLarge
	}
	out := make([]byte, 2+paddedLen(n))
	binary.BigEndian.PutUint16(out, uint16(n)) //nolint:gosec // n <= 65535, checked above
	copy(out[2:], plaintext)
	return out, nil
}

func unpad(padded []byte) ([]byte, error) {
	if len(padded) < 2 {
		return nil, errNIP44Padding
	}
	n := int(binary.BigEndian.Uint16(padded))
	if n < nip44MinPlaintext || len(padded) != 2+paddedLen(n) {
		return nil, errNIP44Padding
	}
	return padded[2 : 2+n], nil
}

// nip44Encrypt encrypts plaintext under a conversation key with a random
// nonce and returns the base64 payload.
func nip44Encrypt(plaintext []byte, conv [32]byte) (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return nip44EncryptWithNonce(plaintext, conv, nonce)
}

func nip44EncryptWithNonce(plaintext []byte, conv [32]byte, nonce []byte) (string, error) {
	chachaKey, chachaNonce, hmacKey, err := messageKeys(conv, nonce)
	if err != nil {
		return "", err
	}
	padded, err := pad(plaintext)
	if err != nil {
		return "", err
	}
	c, err := chacha20.NewUnauthenticatedCipher(chachaKey, chachaNonce)
	if err != nil {
		return "", err
	}
	c.XORKeyStream(padded, padded)

	out := make([]byte, 0, 1+32+len(padded)+32)
	out = append(out, nip44Version)
	out = append(out, nonce...)
	out = append(out, padded...)
	out = append(out, mac(hmacKey, nonce, padded)...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// mac is NIP-44's HMAC-SHA256 over nonce || ciphertext.
func mac(key, nonce, ciphertext []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(nonce)
	m.Write(ciphertext)
	return m.Sum(nil)
}

// nip44Decrypt authenticates and decrypts a base64 payload.
func nip44Decrypt(payload string, conv [32]byte) ([]byte, error) {
	// Bounds from the specification: they cover a 1-byte to 65535-byte
	// plaintext, and are checked before anything is decoded.
	if len(payload) < 132 || len(payload) > 87472 || payload[0] == '#' {
		return nil, errNIP44Payload
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) < 99 || len(raw) > 65603 || raw[0] != nip44Version {
		return nil, errNIP44Payload
	}
	nonce, ciphertext, tag := raw[1:33], raw[33:len(raw)-32], raw[len(raw)-32:]

	chachaKey, chachaNonce, hmacKey, err := messageKeys(conv, nonce)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(tag, mac(hmacKey, nonce, ciphertext)) {
		return nil, errNIP44MAC
	}
	c, err := chacha20.NewUnauthenticatedCipher(chachaKey, chachaNonce)
	if err != nil {
		return nil, err
	}
	padded := make([]byte, len(ciphertext))
	c.XORKeyStream(padded, ciphertext)
	return unpad(padded)
}
