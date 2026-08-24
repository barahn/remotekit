//go:build darwin

package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type darwinServiceManager struct {
	name string
}

// NewServiceManager creates a ServiceManager instance for macOS (launchd).
func NewServiceManager(name string) ServiceManager {
	if name == "" {
		name = "com.mendsec.barahn-agent"
	}
	return &darwinServiceManager{name: name}
}

// IsWindowsService returns false on macOS.
func IsWindowsService() bool {
	return false
}

func (m *darwinServiceManager) plistPath() string {
	return filepath.Join("/Library/LaunchDaemons", fmt.Sprintf("%s.plist", m.name))
}

func (m *darwinServiceManager) Install(cfg Config) error {
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

	plistFile := m.plistPath()
	if _, err := os.Stat(plistFile); err == nil {
		return ErrServiceAlreadyInstalled
	}

	argsXML := fmt.Sprintf("    <string>%s</string>\n", exePath)
	for _, arg := range cfg.Args {
		argsXML += fmt.Sprintf("    <string>%s</string>\n", arg)
	}

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key>
    <%t/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>/var/log/barahn-agent.log</string>
    <key>StandardOutPath</key>
    <string>/var/log/barahn-agent.log</string>
</dict>
</plist>
`, m.name, argsXML, cfg.AutoStart)

	if err := os.WriteFile(plistFile, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("failed to write launchd plist file '%s': %w", plistFile, err)
	}

	if cfg.AutoStart {
		_ = exec.Command("launchctl", "load", "-w", plistFile).Run()
	}

	return nil
}

func (m *darwinServiceManager) Uninstall() error {
	plistFile := m.plistPath()
	if _, err := os.Stat(plistFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	_ = exec.Command("launchctl", "unload", plistFile).Run()
	if err := os.Remove(plistFile); err != nil {
		return fmt.Errorf("failed to remove launchd plist file '%s': %w", plistFile, err)
	}
	return nil
}

func (m *darwinServiceManager) Start() error {
	plistFile := m.plistPath()
	if _, err := os.Stat(plistFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	cmd := exec.Command("launchctl", "start", m.name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to start launchd service '%s': %s (%w)", m.name, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (m *darwinServiceManager) Stop() error {
	plistFile := m.plistPath()
	if _, err := os.Stat(plistFile); os.IsNotExist(err) {
		return ErrServiceNotInstalled
	}

	cmd := exec.Command("launchctl", "stop", m.name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stop launchd service '%s': %s (%w)", m.name, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (m *darwinServiceManager) Status() (Status, error) {
	plistFile := m.plistPath()
	if _, err := os.Stat(plistFile); os.IsNotExist(err) {
		return Status{Installed: false, State: StateStopped}, nil
	}

	out, err := exec.Command("launchctl", "list", m.name).Output()
	if err != nil {
		return Status{Installed: true, State: StateStopped, Description: plistFile}, nil
	}

	return Status{
		Installed:   true,
		State:       StateRunning,
		Description: fmt.Sprintf("launchd job %s\n%s", m.name, strings.TrimSpace(string(out))),
	}, nil
}

func (m *darwinServiceManager) Run(ctx context.Context, runner func(ctx context.Context) error) error {
	return runner(ctx)
}
