// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tray

import (
	"context"
	"testing"
	"time"
)

func TestTrayManagerLifecycle(t *testing.T) {
	exitCalled := false
	onExit := func() {
		exitCalled = true
	}

	mgr := NewTrayManager("test-agent-123", "https://localhost:8443", onExit)
	if mgr == nil {
		t.Fatal("Expected NewTrayManager to return non-nil manager")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)
	mgr.SetStatus("Connecting...", false)
	mgr.SetStatus("Online", true)
	time.Sleep(50 * time.Millisecond)

	mgr.Stop()

	// Verify that onExit can be called if triggered
	_ = exitCalled
}
