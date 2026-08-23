//go:build darwin

package tray

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/mendsec/barahn/pkg/clipboard"
)

type darwinTrayManager struct {
	agentID    string
	serverAddr string
	status     string
	online     bool
	onExit     func()

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTrayManager creates a macOS system tray / menu bar manager.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	return &darwinTrayManager{
		agentID:    agentID,
		serverAddr: serverAddr,
		status:     "Online",
		online:     true,
		onExit:     onExit,
	}
}

func (t *darwinTrayManager) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()

	trayCtx, cancel := context.WithCancel(ctx)
	t.ctx = trayCtx
	t.cancel = cancel

	t.sendNotification(
		"🐕 Barahn Agent Connected",
		fmt.Sprintf("Endpoint ID: %s", t.agentID),
		"Remote control daemon active on this Mac.",
	)
}

func (t *darwinTrayManager) SetStatus(status string, online bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
	t.online = online
}

func (t *darwinTrayManager) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *darwinTrayManager) CopyIDToClipboard() {
	clipMgr := clipboard.NewManager()
	if clipMgr != nil {
		_ = clipMgr.SetText(context.Background(), t.agentID)
	}
	t.sendNotification(
		"📋 Endpoint ID Copied",
		t.agentID,
		"Barahn Endpoint ID copied to macOS clipboard.",
	)
}

func (t *darwinTrayManager) sendNotification(title, subtitle, message string) {
	script := fmt.Sprintf(
		`display notification %q with title %q subtitle %q`,
		message, title, subtitle,
	)
	cmd := exec.Command("osascript", "-e", script)
	_ = cmd.Run()
}
