//go:build !windows && !linux && !darwin

package tray

import "context"

type noopTrayManager struct{}

// NewTrayManager creates a no-op tray manager on non-Windows desktop platforms.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	return &noopTrayManager{}
}

func (t *noopTrayManager) Start(ctx context.Context)               {}
func (t *noopTrayManager) SetStatus(status string, online bool)    {}
func (t *noopTrayManager) Stop()                                   {}
