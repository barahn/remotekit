// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package heartbeat_test

import (
	"context"
	"testing"
	"time"

	"github.com/barahn/remotekit/heartbeat"
)

func TestHeartbeatTicker(t *testing.T) {
	called := false
	sendFunc := func(ctx context.Context) error {
		called = true
		return nil
	}

	ticker := heartbeat.NewChirpTicker(50*time.Millisecond, sendFunc)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	ticker.Start(ctx)
	time.Sleep(120 * time.Millisecond)
	ticker.Stop()

	if !called {
		t.Fatalf("Expected chirp sendFunc to be called by ticker")
	}
}

func TestServerMonitor(t *testing.T) {
	mon := heartbeat.NewServerMonitor(3, func(ctx context.Context, agentID string) error {
		return nil
	})

	now := time.Now().UTC()
	recentChirp := now.Add(-10 * time.Second)
	staleChirp := now.Add(-60 * time.Second)

	if mon.IsOffline(&recentChirp) {
		t.Errorf("Recent chirp wrongly marked offline")
	}

	if !mon.IsOffline(&staleChirp) {
		t.Errorf("Stale chirp wrongly marked online")
	}

	if !mon.IsOffline(nil) {
		t.Errorf("Nil chirp timestamp wrongly marked online")
	}
}
