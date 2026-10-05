// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package x11

import (
	"os"
	"sync"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// TestEnableDoesNotRaceWithEventReaders opens a connection that receives a
// steady stream of events - so its reader goroutine keeps reading xgb's global
// constructor maps - and meanwhile opens further connections and enables every
// extension on them. Calling the xgb Init functions here instead of Enable
// makes the race detector fail this test.
func TestEnableDoesNotRaceWithEventReaders(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set — skipping X11 test")
	}

	busy, err := NewConn()
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	defer busy.Close()

	screen := xproto.Setup(busy).DefaultScreen(busy)
	win, err := xproto.NewWindowId(busy)
	if err != nil {
		t.Fatalf("NewWindowId: %v", err)
	}
	if err := xproto.CreateWindowChecked(busy, screen.RootDepth, win, screen.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// Every ChangeProperty on the window comes back as a PropertyNotify event,
	// which busy's reader decodes through xgb.NewEventFuncs.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			xproto.ChangeProperty(busy, xproto.PropModeReplace, win,
				xproto.AtomWmName, xproto.AtomString, 8, 1, []byte{'x'})
		}
	}()
	go func() {
		defer wg.Done()
		for {
			ev, err := busy.WaitForEvent()
			if ev == nil && err == nil {
				return // connection closed
			}
		}
	}()

	for i := 0; i < 20; i++ {
		conn, err := NewConn()
		if err != nil {
			t.Fatalf("NewConn: %v", err)
		}
		for name := range extensionInits {
			if err := Enable(conn, name); err != nil {
				conn.Close()
				t.Fatalf("Enable(%s): %v", name, err)
			}
		}
		requireOpcode(t, conn, RANDR)
		conn.Close()
	}

	close(stop)
	busy.Close()
	wg.Wait()
}

func requireOpcode(t *testing.T, conn *xgb.Conn, name string) {
	t.Helper()
	conn.ExtLock.RLock()
	defer conn.ExtLock.RUnlock()
	if conn.Extensions[name] == 0 {
		t.Fatalf("extension %s has no opcode after Enable", name)
	}
}

func TestEnableRejectsUnregisteredExtension(t *testing.T) {
	if err := Enable(nil, "GLX"); err == nil {
		t.Fatal("Enable accepted an extension NewConn never registered")
	}
}
