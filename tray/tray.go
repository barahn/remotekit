// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tray

import "context"

// TrayManager defines the lifecycle of the system tray icon on desktop platforms.
type TrayManager interface {
	Start(ctx context.Context)
	SetStatus(status string, online bool)
	Stop()
}
