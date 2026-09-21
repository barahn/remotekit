// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package tray

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestLinuxDBusMenuServer(t *testing.T) {
	var exitCalled atomic.Bool
	onExit := func() {
		exitCalled.Store(true)
	}

	rawMgr := NewTrayManager("agent-abc-456", "https://control.barahn.internal:8443", onExit)
	lt, ok := rawMgr.(*linuxTrayManager)
	if !ok {
		t.Fatalf("Expected *linuxTrayManager, got %T", rawMgr)
	}

	menu := lt.menu
	if menu == nil {
		t.Fatal("Expected non-nil dbusMenuServer")
	}

	// 1. Test GetLayout
	rev, layout, err := menu.GetLayout(0, -1, nil)
	if err != nil {
		t.Fatalf("GetLayout returned error: %v", err)
	}
	if rev < 1 {
		t.Errorf("Expected revision >= 1, got %d", rev)
	}
	if layout.ID != cmdRoot {
		t.Errorf("Expected layout ID %d, got %d", cmdRoot, layout.ID)
	}
	if len(layout.Children) != 9 {
		t.Fatalf("Expected 9 menu children, got %d", len(layout.Children))
	}

	// Verify Header item
	headerChild := layout.Children[0].(dbusMenuLayout)
	if headerChild.ID != cmdHeader {
		t.Errorf("Expected first child to be cmdHeader, got %d", headerChild.ID)
	}
	headerLabel := headerChild.Properties["label"].Value().(string)
	if headerLabel != "Barahn Endpoint Agent" {
		t.Errorf("Unexpected header label: %s", headerLabel)
	}

	// Verify Status item
	statusChild := layout.Children[1].(dbusMenuLayout)
	statusLabel := statusChild.Properties["label"].Value().(string)
	if statusLabel != "Status: Connecting..." {
		t.Errorf("Unexpected initial status label: %s", statusLabel)
	}

	// Verify ID item
	idChild := layout.Children[2].(dbusMenuLayout)
	idLabel := idChild.Properties["label"].Value().(string)
	if idLabel != "ID: agent-abc-456" {
		t.Errorf("Unexpected ID label: %s", idLabel)
	}

	// Verify Server item
	serverChild := layout.Children[3].(dbusMenuLayout)
	serverLabel := serverChild.Properties["label"].Value().(string)
	if serverLabel != "Server: https://control.barahn.internal:8443" {
		t.Errorf("Unexpected server label: %s", serverLabel)
	}

	// 2. Test GetProperty & GetGroupProperties
	propVal, err := menu.GetProperty(cmdCopyID, "label")
	if err != nil {
		t.Fatalf("GetProperty returned error: %v", err)
	}
	if propVal.Value().(string) != "Copy Endpoint ID" {
		t.Errorf("Unexpected CopyID label: %v", propVal.Value())
	}

	groupProps, err := menu.GetGroupProperties([]int32{cmdHeader, cmdExit}, nil)
	if err != nil {
		t.Fatalf("GetGroupProperties returned error: %v", err)
	}
	if len(groupProps) != 2 {
		t.Fatalf("Expected 2 group props, got %d", len(groupProps))
	}

	// 3. Test SetStatus update and Layout change
	lt.SetStatus("Reconnecting...", false)
	_, updatedLayout, err := menu.GetLayout(0, -1, nil)
	if err != nil {
		t.Fatalf("GetLayout after status update failed: %v", err)
	}
	updatedStatusChild := updatedLayout.Children[1].(dbusMenuLayout)
	updatedStatusLabel := updatedStatusChild.Properties["label"].Value().(string)
	if updatedStatusLabel != "Status: Reconnecting..." {
		t.Errorf("Expected updated status label 'Status: Reconnecting...', got %s", updatedStatusLabel)
	}

	// 4. Test Event handling (Exit)
	err = menu.Event(cmdExit, "clicked", dbus.MakeVariant(0), 0)
	if err != nil {
		t.Errorf("Event returned error: %v", err)
	}

	// Wait briefly for exit handler goroutine
	time.Sleep(50 * time.Millisecond)
	if !exitCalled.Load() {
		t.Error("Expected onExit callback to be invoked on cmdExit clicked event")
	}

	// 5. Test AboutToShow / EventGroup
	needUpdate, err := menu.AboutToShow(0)
	if err != nil || needUpdate {
		t.Errorf("Unexpected AboutToShow result: needUpdate=%v, err=%v", needUpdate, err)
	}

	_, err = menu.EventGroup([]dbusMenuEvent{
		{ID: cmdCopyID, EventID: "clicked", Data: dbus.MakeVariant(0), Timestamp: 0},
	})
	if err != nil {
		t.Errorf("EventGroup returned error: %v", err)
	}
}
