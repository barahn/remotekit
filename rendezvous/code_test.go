// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package rendezvous_test

import (
	"errors"
	"testing"

	"github.com/barahn/remotekit/rendezvous"
)

func TestNormalizeAcceptsWhatPeopleType(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		err      error
	}{
		{"123456", "123456", nil},
		{"123-456", "123456", nil},
		{" 123 456 ", "123456", nil},
		{"123.456", "123456", nil},
		{"12345", "", rendezvous.ErrMalformed},   // too short
		{"1234567", "", rendezvous.ErrMalformed}, // too long
		{"12345a", "", rendezvous.ErrMalformed},  // not a digit
		{"", "", rendezvous.ErrMalformed},
		// Digits from other scripts are digits to unicode.IsDigit, but no code
		// contains them; accepting them would give one code two spellings.
		{"١٢٣٤٥٦", "", rendezvous.ErrMalformed},
	} {
		got, err := rendezvous.Normalize(tc.in)
		if !errors.Is(err, tc.err) {
			t.Errorf("Normalize(%q) error = %v, want %v", tc.in, err, tc.err)
			continue
		}
		if tc.err == nil && string(got) != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatSplitsForReadingAloud(t *testing.T) {
	if got := rendezvous.Code("847291").Format(); got != "847-291" {
		t.Errorf("Format = %q, want 847-291", got)
	}
	// What a person reads aloud must normalise back to the same code.
	back, err := rendezvous.Normalize(rendezvous.Code("847291").Format())
	if err != nil || back != "847291" {
		t.Errorf("round trip = %q, %v", back, err)
	}
	if got := rendezvous.Code("nonsense").Format(); got != "nonsense" {
		t.Errorf("Format of a malformed code = %q, want it unchanged", got)
	}
}
