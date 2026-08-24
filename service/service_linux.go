//go:build linux

package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type linuxServiceManager struct {
	name string
}

// NewServiceManager creates a ServiceManager instance for Linux (systemd).
func NewServiceManager(name string) ServiceManager {
	if name == "" {
		name = "barahn-agent"
	}
	return &linuxServiceManager{name: name}
}

// IsWindowsService returns false on Linux.
func IsWindowsService() bool {
	return false
}

func (m *linuxServiceManager) unitPath() string {
	return filepath.Join("/etc/systemd/system", fmt.Sprintf("%s.service", m.name))
}

func (m *linuxServiceManager) Install(cfg Config) error {
	exePath := cfg.ExecPath
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
	}
	exePath, err := filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute executable path: %w", err)
	}

	unitFile := m.unitPath()
	if _, err := os.Stat(unitFile); err == nil {
		return ErrServiceAlreadyInstalled
	}

	description := cfg.Description
	if description == "" {
		description = "Barahn Remote Access Agent Daemon"
	}

	execCommand := exePath
	if len(cfg.Args) > 0 {
		execCommand = fmt.Sprintf("%s %s", exePath, ParseArgsString(cfg.Args))
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "/etc/barahn"
	}

	unitContent := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Restart=always
RestartSec=5s
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, description, execCommand, workDir)

	if err := os.WriteFile(unitFile, []byte(unitContent), 0600); err != nil {
		return fmt.Errorf("failed to write systemd unit file '%s': %w", unitFile, err)
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	if cfg.AutoStart {
		_ = exec.Command("systemctl", "enable", m.name).Run() // #nosec G204 -- m.name is internal service identifier
	}

	return nil
}

func (m *linuxServiceManager) Uninstall() error {
	unitFile := m.unitPath()
	if _, err := os.Stat(unitFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	_ = exec.Command("systemctl", "stop", m.name).Run()    // #nosec G204 -- m.name is internal service identifier
	_ = exec.Command("systemctl", "disable", m.name).Run() // #nosec G204 -- m.name is internal service identifier

	if err := os.Remove(unitFile); err != nil {
		return fmt.Errorf("failed to remove systemd unit file '%s': %w", unitFile, err)
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

func (m *linuxServiceManager) Start() error {
	unitFile := m.unitPath()
	if _, err := os.Stat(unitFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	cmd := exec.Command("systemctl", "start", m.name) // #nosec G204 -- m.name is internal service identifier
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to start systemd service '%s': %s (%w)", m.name, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (m *linuxServiceManager) Stop() error {
	unitFile := m.unitPath()
	if _, err := os.Stat(unitFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	cmd := exec.Command("systemctl", "stop", m.name) // #nosec G204 -- m.name is internal service identifier
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stop systemd service '%s': %s (%w)", m.name, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (m *linuxServiceManager) Status() (Status, error) {
	unitFile := m.unitPath()
	if _, err := os.Stat(unitFile); os.IsNotExist(err) {
		return Status{Installed: false, State: StateStopped}, nil
	}

	out, _ := exec.Command("systemctl", "is-active", m.name).Output() // #nosec G204 -- m.name is internal service identifier
	activeState := strings.TrimSpace(string(out))

	var state State
	switch activeState {
	case "active":
		state = StateRunning
	case "inactive", "failed":
		state = StateStopped
	case "activating":
		state = State("StartPending")
	case "deactivating":
		state = State("StopPending")
	default:
		state = StateUnknown
	}

	var pid int
	pidOut, err := exec.Command("systemctl", "show", "--property", "MainPID", "--value", m.name).Output() // #nosec G204 -- m.name is internal service identifier
	if err == nil {
		if parsedPid, err := strconv.Atoi(strings.TrimSpace(string(pidOut))); err == nil && parsedPid > 0 {
			pid = parsedPid
		}
	}

	return Status{
		Installed:   true,
		State:       state,
		PID:         pid,
		Description: fmt.Sprintf("systemd unit %s", m.unitPath()),
	}, nil
}

func (m *linuxServiceManager) Run(ctx context.Context, runner func(ctx context.Context) error) error {
	return runner(ctx)
}
