//go:build linux

package tray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/mendsec/barahn/pkg/clipboard"
)


// HideConsoleWindow is a no-op on non-Windows platforms.
func HideConsoleWindow() {}

// ShowConsoleWindow is a no-op on non-Windows platforms.
func ShowConsoleWindow() {}

// IsConsoleVisible returns true on non-Windows platforms.
func IsConsoleVisible() bool { return true }

// ToggleConsoleWindow is a no-op on non-Windows platforms.
func ToggleConsoleWindow() {}


const (
	cmdRoot          int32 = 0
	cmdHeader        int32 = 1
	cmdStatus        int32 = 2
	cmdID            int32 = 3
	cmdServer        int32 = 4
	cmdSep1          int32 = 5
	cmdCopyID        int32 = 6
	cmdOpenDashboard int32 = 7
	cmdToggleService int32 = 8
	cmdSep2          int32 = 9
	cmdExit          int32 = 10
)

type dbusMenuLayout struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []interface{}
}

type dbusMenuGroupProps struct {
	ID         int32
	Properties map[string]dbus.Variant
}

type dbusMenuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// dbusMenuServer implements the com.canonical.dbusmenu DBus interface.
type dbusMenuServer struct {
	tray     *linuxTrayManager
	mu       sync.RWMutex
	revision uint32
}

func newDBusMenuServer(tray *linuxTrayManager) *dbusMenuServer {
	return &dbusMenuServer{
		tray:     tray,
		revision: 1,
	}
}

func (m *dbusMenuServer) getItemProps(id int32) map[string]dbus.Variant {
	m.tray.mu.Lock()
	agentID := m.tray.agentID
	serverAddr := m.tray.serverAddr
	status := m.tray.status
	online := m.tray.online
	m.tray.mu.Unlock()

	props := make(map[string]dbus.Variant)

	switch id {
	case cmdRoot:
		props["children-display"] = dbus.MakeVariant("submenu")
	case cmdHeader:
		props["label"] = dbus.MakeVariant("🐕 Barahn Endpoint Agent")
		props["enabled"] = dbus.MakeVariant(false)
		props["visible"] = dbus.MakeVariant(true)
	case cmdStatus:
		statusIcon := "🟢"
		if !online {
			statusIcon = "🔴"
		}
		props["label"] = dbus.MakeVariant(fmt.Sprintf("%s Status: %s", statusIcon, status))
		props["enabled"] = dbus.MakeVariant(false)
		props["visible"] = dbus.MakeVariant(true)
	case cmdID:
		props["label"] = dbus.MakeVariant(fmt.Sprintf("🆔 ID: %s", agentID))
		props["enabled"] = dbus.MakeVariant(false)
		props["visible"] = dbus.MakeVariant(true)
	case cmdServer:
		props["label"] = dbus.MakeVariant(fmt.Sprintf("🌐 Server: %s", serverAddr))
		props["enabled"] = dbus.MakeVariant(false)
		props["visible"] = dbus.MakeVariant(true)
	case cmdSep1:
		props["type"] = dbus.MakeVariant("separator")
		props["visible"] = dbus.MakeVariant(true)
	case cmdCopyID:
		props["label"] = dbus.MakeVariant("📋 Copy Endpoint ID")
		props["enabled"] = dbus.MakeVariant(true)
		props["visible"] = dbus.MakeVariant(true)
	case cmdOpenDashboard:
		props["label"] = dbus.MakeVariant("🌐 Open Web Dashboard")
		props["enabled"] = dbus.MakeVariant(true)
		props["visible"] = dbus.MakeVariant(true)
	case cmdToggleService:
		props["label"] = dbus.MakeVariant("⚙️ Toggle Background Service")
		props["enabled"] = dbus.MakeVariant(true)
		props["visible"] = dbus.MakeVariant(true)
	case cmdSep2:
		props["type"] = dbus.MakeVariant("separator")
		props["visible"] = dbus.MakeVariant(true)
	case cmdExit:
		props["label"] = dbus.MakeVariant("❌ Exit Agent")
		props["enabled"] = dbus.MakeVariant(true)
		props["visible"] = dbus.MakeVariant(true)
	}

	return props
}

func (m *dbusMenuServer) GetLayout(parentId int32, recursionDepth int32, propertyNames []string) (uint32, dbusMenuLayout, *dbus.Error) {
	m.mu.RLock()
	rev := m.revision
	m.mu.RUnlock()

	rootProps := m.getItemProps(cmdRoot)
	childrenIDs := []int32{cmdHeader, cmdStatus, cmdID, cmdServer, cmdSep1, cmdCopyID, cmdOpenDashboard, cmdToggleService, cmdSep2, cmdExit}

	var children []interface{}
	for _, cid := range childrenIDs {
		childProps := m.getItemProps(cid)
		children = append(children, dbusMenuLayout{
			ID:         cid,
			Properties: childProps,
			Children:   []interface{}{},
		})
	}

	layout := dbusMenuLayout{
		ID:         cmdRoot,
		Properties: rootProps,
		Children:   children,
	}

	return rev, layout, nil
}

func (m *dbusMenuServer) GetGroupProperties(ids []int32, propertyNames []string) ([]dbusMenuGroupProps, *dbus.Error) {
	var result []dbusMenuGroupProps
	for _, id := range ids {
		result = append(result, dbusMenuGroupProps{
			ID:         id,
			Properties: m.getItemProps(id),
		})
	}
	return result, nil
}

func (m *dbusMenuServer) GetProperty(id int32, propertyName string) (dbus.Variant, *dbus.Error) {
	props := m.getItemProps(id)
	if val, ok := props[propertyName]; ok {
		return val, nil
	}
	return dbus.MakeVariant(""), nil
}

func (m *dbusMenuServer) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID == "clicked" {
		switch id {
		case cmdCopyID:
			go m.tray.copyIDToClipboard()
		case cmdOpenDashboard:
			go m.tray.openDashboard()
		case cmdExit:
			go func() {
				if m.tray.onExit != nil {
					m.tray.onExit()
				}
				m.tray.Stop()
			}()
		}
	}
	return nil
}


func (m *dbusMenuServer) EventGroup(events []dbusMenuEvent) ([]int32, *dbus.Error) {
	for _, ev := range events {
		_ = m.Event(ev.ID, ev.EventID, ev.Data, ev.Timestamp)
	}
	return nil, nil
}

func (m *dbusMenuServer) AboutToShow(id int32) (bool, *dbus.Error) {
	return false, nil
}

func (m *dbusMenuServer) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return nil, nil, nil
}

func (m *dbusMenuServer) notifyUpdate() {
	m.mu.Lock()
	m.revision++
	rev := m.revision
	m.mu.Unlock()

	conn := m.tray.getConn()
	if conn != nil {
		_ = conn.Emit("/MenuBar", "com.canonical.dbusmenu.LayoutUpdated", rev, int32(0))
	}
}

type linuxTrayManager struct {
	agentID    string
	serverAddr string
	status     string
	online     bool
	onExit     func()

	mu     sync.Mutex
	conn   *dbus.Conn
	props  *prop.Properties
	menu   *dbusMenuServer
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTrayManager creates a Linux StatusNotifierItem + DBusMenu system tray manager.
func NewTrayManager(agentID, serverAddr string, onExit func()) TrayManager {
	lt := &linuxTrayManager{
		agentID:    agentID,
		serverAddr: serverAddr,
		status:     "Online",
		online:     true,
		onExit:     onExit,
	}
	lt.menu = newDBusMenuServer(lt)
	return lt
}

func (t *linuxTrayManager) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()

	trayCtx, cancel := context.WithCancel(ctx)
	t.ctx = trayCtx
	t.cancel = cancel

	go t.run(trayCtx)
}

func (t *linuxTrayManager) getConn() *dbus.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn
}

func (t *linuxTrayManager) run(ctx context.Context) {
	conn, err := dbus.SessionBus()
	if err != nil {
		// Session bus not available (headless environment)
		return
	}

	serviceName := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(serviceName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		_ = conn.Close()
		return
	}

	// 1. Export com.canonical.dbusmenu on /MenuBar
	if err := conn.Export(t.menu, "/MenuBar", "com.canonical.dbusmenu"); err != nil {
		// DBusMenu export non-fatal, continue with SNI
		_ = err
	}

	iconThemePath := ""
	if home, err := os.UserHomeDir(); err == nil {
		localIconDir := fmt.Sprintf("%s/.local/share/icons/hicolor", home)
		if _, err := os.Stat(localIconDir); err == nil {
			iconThemePath = localIconDir
		}
	}

	iconName := "barahn"
	if iconThemePath == "" {
		iconName = "preferences-desktop-remote-desktop"
	}

	// 2. Export StatusNotifierItem Properties on /StatusNotifierItem
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
				Value:    iconName,
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"IconThemePath": {
				Value:    iconThemePath,
				Writable: false,
				Emit:     prop.EmitTrue,
			},
			"Menu": {
				Value:    dbus.ObjectPath("/MenuBar"),
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
		_ = conn.Close()
		return
	}

	t.mu.Lock()
	t.conn = conn
	t.props = props
	t.mu.Unlock()

	// 3. Register with StatusNotifierWatcher if present (GNOME/KDE/XFCE)
	watcher := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	_ = watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, "/StatusNotifierItem").Store()

	// 4. Send native desktop notification on connection
	t.sendDesktopNotification(
		"🐕 Barahn Agent Connected",
		fmt.Sprintf("Endpoint ID: %s\nRemote control daemon active on this workstation.", t.agentID),
	)

	<-ctx.Done()

	t.mu.Lock()
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
	t.props = nil
	t.mu.Unlock()
}

func (t *linuxTrayManager) SetStatus(status string, online bool) {
	t.mu.Lock()
	t.status = status
	t.online = online
	props := t.props
	t.mu.Unlock()

	if props != nil {
		icon := "preferences-desktop-remote-desktop"
		if !online {
			icon = "dialog-warning"
		}
		_ = props.Set("org.kde.StatusNotifierItem", "IconName", dbus.MakeVariant(icon))
		_ = props.Set("org.kde.StatusNotifierItem", "ToolTip", dbus.MakeVariant([]interface{}{
			icon,
			[]dbus.Variant{},
			fmt.Sprintf("Barahn Agent: %s (%s)", t.agentID, status),
			fmt.Sprintf("Server: %s", t.serverAddr),
		}))
	}

	if t.menu != nil {
		t.menu.notifyUpdate()
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
		t.conn = nil
	}
	t.props = nil
}

func (t *linuxTrayManager) copyIDToClipboard() {
	clipMgr := clipboard.NewManager()
	if clipMgr != nil {
		_ = clipMgr.SetText(context.Background(), t.agentID)
	}

	t.sendDesktopNotification(
		"📋 Endpoint ID Copied",
		fmt.Sprintf("Barahn Endpoint ID %s copied to clipboard.", t.agentID),
	)
}

func (t *linuxTrayManager) sendDesktopNotification(summary, body string) {
	conn := t.getConn()
	if conn == nil {
		return
	}

	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	hints := map[string]dbus.Variant{
		"urgency": dbus.MakeVariant(byte(1)), // Normal
	}

	_ = obj.Call(
		"org.freedesktop.Notifications.Notify",
		0,
		"Barahn Agent",
		uint32(0),
		"preferences-desktop-remote-desktop",
		summary,
		body,
		[]string{},
		hints,
		int32(5000),
	).Store()
}

func (t *linuxTrayManager) openDashboard() {
	url := t.serverAddr
	if url == "" {
		url = "https://localhost:8443"
	}
	_ = exec.Command("xdg-open", url).Start() // #nosec G204 -- url is configured server address
}

