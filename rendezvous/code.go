// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package rendezvous

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// Code is a short numeric code by which a peer can be reached: six digits, in
// the range 100000-999999 so that it never needs a leading zero read aloud.
type Code string

// CodeLength is the number of digits in a Code.
const CodeLength = 6

var (
	// ErrMalformed reports input that is not six digits once separators are
	// removed. It is a failed attempt like any other: see Registry.Redeem.
	ErrMalformed = errors.New("rendezvous: a code is six digits")
)

// newCode draws a Code from crypto/rand. 900,000 values.
func newCode() (Code, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", fmt.Errorf("rendezvous: drawing a code: %w", err)
	}
	return Code(fmt.Sprintf("%06d", n.Int64()+100000)), nil
}

// Format returns c split for reading aloud: "847291" becomes "847-291". A
// value that is not a well-formed code is returned unchanged.
func (c Code) Format() string {
	n, err := Normalize(string(c))
	if err != nil {
		return string(c)
	}
	return string(n[:3]) + "-" + string(n[3:])
}

// Normalize turns what a person typed into a Code, accepting the separators
// people add when they copy or transcribe one -- spaces, hyphens, dots -- and
// nothing else.
func Normalize(s string) (Code, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r), r == '-', r == '.':
			continue
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			// unicode.IsDigit would accept other scripts' digits, which
			// no code ever contains; refusing them keeps one spelling per
			// code.
			return "", ErrMalformed
		}
	}
	if b.Len() != CodeLength {
		return "", ErrMalformed
	}
	return Code(b.String()), nil
}
