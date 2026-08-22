//go:build linux

package tray

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

type linuxTrayManager struct {
	agentID    string
	serverAddr string
	status     string
	online     bool
	onExit     func()

	mu     sync.Mutex
	conn   *dbus.Conn
	props  *prop.Properties
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTrayManager creates a Linux StatusNotifierItem system tray manager.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	return &linuxTrayManager{
		agentID:    agentID,
		serverAddr: serverAddr,
		status:     "Starting...",
		online:     false,
		onExit:     onExit,
	}
}

func (t *linuxTrayManager) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()

	trayCtx, cancel := context.WithCancel(ctx)
	t.ctx = trayCtx
	t.cancel = cancel

	go t.run(trayCtx)
}

func (t *linuxTrayManager) run(ctx context.Context) {
	conn, err := dbus.SessionBus()
	if err != nil {
		// Session bus not available (e.g. headless environment or headless test container)
		return
	}
	t.conn = conn

	serviceName := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(serviceName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return
	}

	sniProps := map[string]map[string]*prop.Prop{
		"org.kde.StatusNotifierItem": {
			"Category": {
				Value:    "ApplicationStatus",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"Id": {
				Value:    "barahn-agent",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"Title": {
				Value:    "Barahn Remote Agent",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"Status": {
				Value:    "Active",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"IconName": {
				Value:    "preferences-desktop-remote-desktop",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"IconThemePath": {
				Value:    "",
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"ToolTip": {
				Value:    []interface{}{"preferences-desktop-remote-desktop", []dbus.Variant{}, fmt.Sprintf("Barahn Agent: %s", t.agentID), fmt.Sprintf("Connected to %s", t.serverAddr)},
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"ItemIsMenu": {
				Value:    false,
				Writable: false,
				Emit:     prop.EmitTrue,
			},
		},
	}

	props, err := prop.Export(conn, "/StatusNotifierItem", sniProps)
	if err != nil {
		return
	}
	t.props = props

	// Register with StatusNotifierWatcher if present in desktop environment
	watcher := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	_ = watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, "/StatusNotifierItem").Store()

	<-ctx.Done()
	_ = conn.Close()
}

func (t *linuxTrayManager) SetStatus(status string, online bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
	t.online = online

	if t.props != nil {
		icon := "preferences-desktop-remote-desktop"
		if !online {
			icon = "dialog-warning"
		}
		_ = t.props.Set("org.kde.StatusNotifierItem", "IconName", dbus.MakeVariant(icon))
		_ = t.props.Set("org.kde.StatusNotifierItem", "ToolTip", dbus.MakeVariant([]interface{}{
			icon,
			[]dbus.Variant{},
			fmt.Sprintf("Barahn Agent: %s (%s)", t.agentID, status),
			fmt.Sprintf("Server: %s", t.serverAddr),
		}))
	}
}

func (t *linuxTrayManager) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	if t.conn != nil {
		_ = t.conn.Close()
	}
}
