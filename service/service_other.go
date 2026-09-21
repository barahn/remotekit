// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !windows && !linux && !darwin

package service

import (
	"context"
)

type noopServiceManager struct{}

// NewServiceManager creates a fallback stub ServiceManager.
func NewServiceManager(name string) ServiceManager {
	return &noopServiceManager{}
}

// IsWindowsService returns false on other platforms.
func IsWindowsService() bool {
	return false
}

func (m *noopServiceManager) Install(cfg Config) error {
	return ErrUnsupportedPlatform
}

func (m *noopServiceManager) Uninstall() error {
	return ErrUnsupportedPlatform
}

func (m *noopServiceManager) Start() error {
	return ErrUnsupportedPlatform
}

func (m *noopServiceManager) Stop() error {
	return ErrUnsupportedPlatform
}

func (m *noopServiceManager) Status() (Status, error) {
	return Status{Installed: false, State: StateUnknown}, ErrUnsupportedPlatform
}

func (m *noopServiceManager) Run(ctx context.Context, runner func(ctx context.Context) error) error {
	return runner(ctx)
}
