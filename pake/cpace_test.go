// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package pake

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/gtank/ristretto255"
)

// The vectors in this file are copied from draft-irtf-cfrg-cpace-21,
// appendix A.1 and B.3 (CPace using ristretto255 and SHA-512).

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func scalar(t *testing.T, s string) *ristretto255.Scalar {
	t.Helper()
	y, err := ristretto255.NewScalar().SetCanonicalBytes(unhex(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return y
}

func countUp(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// A.1.2 and A.1.4.
func TestPrependLenAndLvCat(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{nil, "00"},
		{[]byte("1234"), "0431323334"},
		{countUp(127), "7f" + hex.EncodeToString(countUp(127))},
		{countUp(128), "8001" + hex.EncodeToString(countUp(128))},
	} {
		if got := hex.EncodeToString(prependLen(tc.in)); got != tc.want {
			t.Errorf("prependLen(%d bytes) = %s, want %s", len(tc.in), got, tc.want)
		}
	}
	got := lvCat([]byte("1234"), []byte("5"), nil, []byte("678"))
	if want := unhex(t, "043132333401350003363738"); !bytes.Equal(got, want) {
		t.Errorf("lvCat = %x, want %x", got, want)
	}
}

// B.3 inputs.
var (
	vecPRS = []byte("Password")
	vecCI  = []byte("\x0bA_initiator\x0bB_responder")
	vecADa = []byte("ADa")
	vecADb = []byte("ADb")
)

const (
	vecSid = "7e4b4791d6a8ef019b936c79fb7f2c57"
	vecYa  = `da3d23700a9e5699258aef94dc060dfda5ebb61f02a5ea77fad53f4f
	          f0976d08`
	vecYb = `d2316b454718c35362d83d69df6320f38578ed5984651435e2949762
	          d900b80d`
)

// B.3.1 to B.3.5.
func TestCPaceRistretto255Vectors(t *testing.T) {
	sid := unhex(t, vecSid)

	gs := generatorString(vecPRS, vecCI, sid)
	wantGS := unhex(t, `11435061636552697374726574746f3235350850617373776f726464
		00000000000000000000000000000000000000000000000000000000
		00000000000000000000000000000000000000000000000000000000
		00000000000000000000000000000000000000000000000000000000
		00000000000000000000000000000000180b415f696e69746961746f
		720b425f726573706f6e646572107e4b4791d6a8ef019b936c79fb7f
		2c57`)
	if !bytes.Equal(gs, wantGS) {
		t.Fatalf("generator_string =\n%x\nwant\n%x", gs, wantGS)
	}
	h := sha512.Sum512(gs)
	if want := unhex(t, `da6d3ddc8802fca9058755ffd3ebde08a9c2c74945901a258482a288
		b6663af06bf645c93cd1c51512307199c80e84908916d983b34af772
		05f90851a657ee27`); !bytes.Equal(h[:], want) {
		t.Fatalf("hash of generator_string = %x", h)
	}
	g := calculateGenerator(vecPRS, vecCI, sid)
	if want := unhex(t, "222b6b195fe84b1652badb6f6a3ae3d24341e7306967f0b8115b40d5698c7e56"); !bytes.Equal(g.Bytes(), want) {
		t.Fatalf("generator = %x, want %x", g.Bytes(), want)
	}

	ya, yb := scalar(t, vecYa), scalar(t, vecYb)
	Ya, Yb := scalarMult(ya, g), scalarMult(yb, g)
	if want := unhex(t, "d6bac480f2c386c394efc7c47adb9925dcd2630b64f240c50f8d0eec482b9157"); !bytes.Equal(Ya, want) {
		t.Errorf("Ya = %x, want %x", Ya, want)
	}
	if want := unhex(t, "3ea7e0b19560d7c0b0f5734f63b955286dfa8232b5ebe63324e2d9e7433f7258"); !bytes.Equal(Yb, want) {
		t.Errorf("Yb = %x, want %x", Yb, want)
	}

	wantK := unhex(t, "80b69a8a76457ab6a4d7f887a4bf6b55a2f80ac19c333f917a05fc9887c8b40f")
	ka, kb := scalarMultVfy(ya, Yb), scalarMultVfy(yb, Ya)
	if !bytes.Equal(ka, wantK) || !bytes.Equal(kb, wantK) {
		t.Errorf("K = %x and %x, want %x", ka, kb, wantK)
	}

	if want := unhex(t, `20d6bac480f2c386c394efc7c47adb9925dcd2630b64f240c50f8d0e
		ec482b915703414461203ea7e0b19560d7c0b0f5734f63b955286dfa
		8232b5ebe63324e2d9e7433f725803414462`); !bytes.Equal(transcriptIR(Ya, vecADa, Yb, vecADb), want) {
		t.Errorf("transcript_ir = %x", transcriptIR(Ya, vecADa, Yb, vecADb))
	}
	got, err := isk(sid, ka, Ya, vecADa, Yb, vecADb)
	if err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, `b69effbf61b51d56401c0f65601abe428de8206feaaf0e32198896dc
		ae7b35cd2b38950a39dfd5d4a79164614c2984f7daa460b588c1e80c
		3fa2068af7900447`); !bytes.Equal(got, want) {
		t.Errorf("ISK = %x, want %x", got, want)
	}
}

// B.3.10 and B.3.11.
func TestScalarMultVfyVectors(t *testing.T) {
	s := scalar(t, "7cd0e075fa7955ba52c02759a6c90dbbfc10e6d40aea8d283e407d88cf538a05")
	x := unhex(t, "2c3c6b8c4f3800e7aef6864025b4ed79bd599117e427c41bd47d93d654b4a51c")
	want := unhex(t, "7c13645fe790a468f62c39beb7388e541d8405d1ade69d1778c5fe3e7f6b600e")
	if got := scalarMultVfy(s, x); !bytes.Equal(got, want) {
		t.Errorf("scalar_mult_vfy(s, X) = %x, want %x", got, want)
	}
	p, err := ristretto255.NewIdentityElement().SetCanonicalBytes(x)
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarMult(s, p); !bytes.Equal(got, want) {
		t.Errorf("scalar_mult(s, decode(X)) = %x, want %x", got, want)
	}

	for _, bad := range []string{
		"2b3c6b8c4f3800e7aef6864025b4ed79bd599117e427c41bd47d93d654b4a51c", // not an encoding
		"0000000000000000000000000000000000000000000000000000000000000000", // G.I
	} {
		k := scalarMultVfy(s, unhex(t, bad))
		if !bytes.Equal(k, identity) {
			t.Errorf("scalar_mult_vfy(s, %s) = %x, want G.I", bad, k)
		}
		if _, err := isk([]byte("sid"), k, nil, nil, nil, nil); err == nil {
			t.Errorf("an ISK was derived from K = G.I (from %s)", bad)
		}
	}
}

// A sampled scalar is below 2^252, so always canonical, as section 8.3 has it.
func TestSampleScalarClearsTheTopBits(t *testing.T) {
	s, err := sampleScalar(bytes.NewReader(bytes.Repeat([]byte{0xff}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if b := s.Bytes(); b[31] != 0x0f {
		t.Errorf("top byte = %#x, want 0x0f", b[31])
	}
}
