// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package clipboard

import (
	"context"
	"errors"
	"testing"
)

func TestDarwinClipboardConcealed(t *testing.T) {
	ctx := context.Background()

	t.Run("sensitive types return concealed", func(t *testing.T) {
		dc := &DarwinClipboard{
			typesReader: func(ctx context.Context) ([]string, error) {
				return []string{"public.utf8-plain-text", darwinConcealedType}, nil
			},
		}
		concealed, err := dc.Concealed(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !concealed {
			t.Fatalf("expected concealed=true for %s, got false", darwinConcealedType)
		}
	})

	t.Run("1Password type returns concealed", func(t *testing.T) {
		dc := &DarwinClipboard{
			typesReader: func(ctx context.Context) ([]string, error) {
				return []string{darwinOnePasswordType}, nil
			},
		}
		concealed, err := dc.Concealed(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !concealed {
			t.Fatalf("expected concealed=true for %s, got false", darwinOnePasswordType)
		}
	})

	t.Run("plain text returns not concealed", func(t *testing.T) {
		dc := &DarwinClipboard{
			typesReader: func(ctx context.Context) ([]string, error) {
				return []string{"public.utf8-plain-text", "NSStringPboardType"}, nil
			},
		}
		concealed, err := dc.Concealed(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if concealed {
			t.Fatalf("expected concealed=false for ordinary text, got true")
		}
	})

	t.Run("error fails closed", func(t *testing.T) {
		dc := &DarwinClipboard{
			typesReader: func(ctx context.Context) ([]string, error) {
				return nil, errors.New("pasteboard unavailable")
			},
		}
		concealed, err := dc.Concealed(ctx)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !concealed {
			t.Fatalf("expected concealed=true on error (fail-closed), got false")
		}
	})
}
