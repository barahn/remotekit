// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type windowsServiceManager struct {
	name string
}

// NewServiceManager creates a ServiceManager instance for Windows.
func NewServiceManager(name string) ServiceManager {
	if name == "" {
		name = "BarahnAgent"
	}
	return &windowsServiceManager{name: name}
}

// IsWindowsService returns true if the current process is running as a Windows Service.
func IsWindowsService() bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isSvc
}

func (m *windowsServiceManager) Install(cfg Config) error {
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

	scManager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Windows Service Control Manager: %w", err)
	}
	defer scManager.Disconnect()

	// Check if already installed
	s, err := scManager.OpenService(m.name)
	if err == nil {
		s.Close()
		return ErrServiceAlreadyInstalled
	}

	displayName := cfg.DisplayName
	if displayName == "" {
		displayName = "Barahn Remote Access Agent"
	}
	description := cfg.Description
	if description == "" {
		description = "Barahn persistent unattended remote support and management agent daemon."
	}

	startType := mgr.StartAutomatic
	if !cfg.AutoStart {
		startType = mgr.StartManual
	}

	mgrConfig := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    uint32(startType),
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  displayName,
		Description:  description,
	}

	serviceArgs := cfg.Args
	s, err = scManager.CreateService(m.name, exePath, mgrConfig, serviceArgs...)
	if err != nil {
		return fmt.Errorf("failed to create Windows service '%s': %w", m.name, err)
	}
	defer s.Close()

	// Set recovery actions: restart service on failure
	recoveryActions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	_ = s.SetRecoveryActions(recoveryActions, 86400) // Reset failure count after 24 hours

	return nil
}

func (m *windowsServiceManager) Uninstall() error {
	scManager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer scManager.Disconnect()

	s, err := scManager.OpenService(m.name)
	if err != nil {
		return ErrServiceNotInstalled
	}
	defer s.Close()

	// Stop before deleting if running
	status, err := s.Query()
	if err == nil && status.State == svc.Running {
		_, _ = s.Control(svc.Stop)
		time.Sleep(1 * time.Second)
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("failed to delete service '%s': %w", m.name, err)
	}

	return nil
}

func (m *windowsServiceManager) Start() error {
	scManager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer scManager.Disconnect()

	s, err := scManager.OpenService(m.name)
	if err != nil {
		return ErrServiceNotInstalled
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("failed to start service '%s': %w", m.name, err)
	}

	return nil
}

func (m *windowsServiceManager) Stop() error {
	scManager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer scManager.Disconnect()

	s, err := scManager.OpenService(m.name)
	if err != nil {
		return ErrServiceNotInstalled
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("failed to send stop signal to service '%s': %w", m.name, err)
	}

	timeout := time.Now().Add(10 * time.Second)
	for status.State != svc.Stopped {
		if time.Now().After(timeout) {
			return fmt.Errorf("timed out waiting for service '%s' to stop", m.name)
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			break
		}
	}

	return nil
}

func (m *windowsServiceManager) Status() (Status, error) {
	scManager, err := mgr.Connect()
	if err != nil {
		return Status{}, fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer scManager.Disconnect()

	s, err := scManager.OpenService(m.name)
	if err != nil {
		return Status{Installed: false, State: StateStopped}, nil
	}
	defer s.Close()

	rawStatus, err := s.Query()
	if err != nil {
		return Status{Installed: true, State: StateUnknown}, fmt.Errorf("failed to query service status: %w", err)
	}

	var state State
	switch rawStatus.State {
	case svc.Running:
		state = StateRunning
	case svc.Stopped:
		state = StateStopped
	case svc.Paused:
		state = StatePaused
	case svc.StartPending:
		state = State("StartPending")
	case svc.StopPending:
		state = State("StopPending")
	default:
		state = StateUnknown
	}

	var pid int
	if rawStatus.ProcessId != 0 {
		pid = int(rawStatus.ProcessId)
	}

	return Status{
		Installed:   true,
		State:       state,
		PID:         pid,
		Description: fmt.Sprintf("Windows Service '%s'", m.name),
	}, nil
}

type windowsServiceHandler struct {
	runner func(ctx context.Context) error
}

func (h *windowsServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- h.runner(ctx)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-errChan:
				case <-time.After(5 * time.Second):
				}
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
			}
		case err := <-errChan:
			changes <- svc.Status{State: svc.Stopped}
			if err != nil && err != context.Canceled {
				return false, uint32(windows.ERROR_EXCEPTION_IN_SERVICE)
			}
			return false, 0
		}
	}
}

func (m *windowsServiceManager) Run(ctx context.Context, runner func(ctx context.Context) error) error {
	handler := &windowsServiceHandler{runner: runner}
	if err := svc.Run(m.name, handler); err != nil {
		return fmt.Errorf("failed to run Windows Service handler for '%s': %w", m.name, err)
	}
	return nil
}
