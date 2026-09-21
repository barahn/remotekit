// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"testing"
)

func TestMemoryClipboard(t *testing.T) {
	ctx := context.Background()
	cb := NewMemoryClipboard()

	text, err := cb.GetText(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty string, got %q", text)
	}

	expected := "Hello Barahn Clipboard!"
	if err := cb.SetText(ctx, expected); err != nil {
		t.Fatalf("unexpected error setting text: %v", err)
	}

	text, err = cb.GetText(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting text: %v", err)
	}
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

func TestLinuxClipboardFallback(t *testing.T) {
	ctx := context.Background()
	cb := NewMemoryClipboard()

	expected := "Sample Linux Text"
	if err := cb.SetText(ctx, expected); err != nil {
		t.Fatalf("SetText failed: %v", err)
	}

	got, err := cb.GetText(ctx)
	if err != nil {
		t.Fatalf("GetText failed: %v", err)
	}
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
