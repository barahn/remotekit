//go:build windows

package clipboard

import (
	"context"
	"testing"
)

func TestWindowsClipboard(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Fatal("expected non-nil clipboard manager")
	}

	ctx := context.Background()

	testStr := "Barahn Windows Clipboard Sync Test 123"
	err := mgr.SetText(ctx, testStr)
	if err != nil {
		t.Fatalf("SetText failed: %v", err)
	}

	got, err := mgr.GetText(ctx)
	if err != nil {
		t.Fatalf("GetText failed: %v", err)
	}

	if got != testStr {
		t.Errorf("GetText got %q, want %q", got, testStr)
	}
}
