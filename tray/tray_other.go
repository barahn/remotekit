// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !windows && !linux && !darwin

package tray

import "context"

type noopTrayManager struct{}

// NewTrayManager creates a no-op tray manager on non-Windows desktop platforms.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	return &noopTrayManager{}
}

func (t *noopTrayManager) Start(ctx context.Context)            {}
func (t *noopTrayManager) SetStatus(status string, online bool) {}
func (t *noopTrayManager) Stop()                                {}

// HideConsoleWindow is a no-op on non-Windows platforms.
func HideConsoleWindow() {}

// ShowConsoleWindow is a no-op on non-Windows platforms.
func ShowConsoleWindow() {}

// IsConsoleVisible returns true on non-Windows platforms.
func IsConsoleVisible() bool { return true }

// ToggleConsoleWindow is a no-op on non-Windows platforms.
func ToggleConsoleWindow() {}
