// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pake

import (
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"io"

	"github.com/gtank/ristretto255"
)

// This file is CPace itself, the cipher suite CPACE-RISTRETTO255-SHA512 of
// draft-irtf-cfrg-cpace-21, section 8.3, word for word: every function here
// is named after the draft's and checked against its test vectors in
// appendix B.3 (cpace_test.go). The group arithmetic is gtank/ristretto255,
// which implements RFC 9496 on filippo.io/edwards25519; nothing here does
// arithmetic of its own.

// dsi is G_Ristretto255.DSI.
var dsi = []byte("CPaceRistretto255")

// hashBlockSize is H.s_in_bytes for SHA-512: its input block size.
const hashBlockSize = 128

// groupSizeBytes is the length of a scalar and of an encoded element.
const groupSizeBytes = 32

// errIdentity is a peer's element that is not a valid encoding, or that
// leads to the identity: the draft says to abort on either.
var errIdentity = errors.New("pake: the peer's element is invalid")

// prependLen is the draft's prepend_len: s with its length in front, encoded
// as LEB128.
func prependLen(s []byte) []byte {
	n := len(s)
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			out = append(out, b)
			break
		}
		out = append(out, b|0x80)
	}
	return append(out, s...)
}

// lvCat is the draft's lv_cat: each argument length-prefixed, concatenated.
func lvCat(args ...[]byte) []byte {
	var out []byte
	for _, a := range args {
		out = append(out, prependLen(a)...)
	}
	return out
}

// generatorString is the draft's generator_string: DSI and PRS, zero-padded
// to fill the hash's first block, then CI and sid, all length-prefixed.
func generatorString(prs, ci, sid []byte) []byte {
	zpad := hashBlockSize - 1 - len(prependLen(prs)) - len(prependLen(dsi))
	if zpad < 0 {
		zpad = 0
	}
	return lvCat(dsi, prs, make([]byte, zpad), ci, sid)
}

// calculateGenerator is G.calculate_generator for ristretto255: the
// generator string hashed to 64 bytes, then mapped into the group with
// RFC 9496's one-way map.
func calculateGenerator(prs, ci, sid []byte) *ristretto255.Element {
	h := sha512.Sum512(generatorString(prs, ci, sid))
	g, err := ristretto255.NewIdentityElement().SetUniformBytes(h[:])
	if err != nil {
		panic("pake: SetUniformBytes refused 64 bytes") // cannot happen
	}
	return g
}

// sampleScalar is G.sample_scalar as the draft recommends it: random bytes
// with the bits above the group's 252 cleared, read little-endian. The result
// is below the group order, so it is always a canonical scalar.
func sampleScalar(rnd io.Reader) (*ristretto255.Scalar, error) {
	var b [groupSizeBytes]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return nil, err
	}
	b[31] &= 0x0f
	return ristretto255.NewScalar().SetCanonicalBytes(b[:])
}

// scalarMult is G.scalar_mult: y times the generator g, encoded.
func scalarMult(y *ristretto255.Scalar, g *ristretto255.Element) []byte {
	return ristretto255.NewIdentityElement().ScalarMult(y, g).Bytes()
}

// identity is G.I, the encoding of the neutral element.
var identity = ristretto255.NewIdentityElement().Bytes()

// scalarMultVfy is G.scalar_mult_vfy: y times the element x encodes, or the
// identity if x does not decode.
func scalarMultVfy(y *ristretto255.Scalar, x []byte) []byte {
	p, err := ristretto255.NewIdentityElement().SetCanonicalBytes(x)
	if err != nil {
		return append([]byte(nil), identity...)
	}
	return ristretto255.NewIdentityElement().ScalarMult(y, p).Bytes()
}

// transcriptIR is the draft's transcript_ir: the initiator's message, then
// the responder's. Chirp has clear roles, so this is the mode used.
func transcriptIR(ya, ada, yb, adb []byte) []byte {
	return append(lvCat(ya, ada), lvCat(yb, adb)...)
}

// isk is the draft's ISK, given K: the hash of the ISK domain separator, sid
// and K, then the transcript. It refuses K = G.I, as the draft requires.
func isk(sid, k, ya, ada, yb, adb []byte) ([]byte, error) {
	if subtleEqual(k, identity) {
		return nil, errIdentity
	}
	h := sha512.New()
	h.Write(lvCat(append(append([]byte(nil), dsi...), "_ISK"...), sid, k))
	h.Write(transcriptIR(ya, ada, yb, adb))
	return h.Sum(nil), nil
}

// randReader is where scalars come from; tests replace it.
var randReader io.Reader = rand.Reader
