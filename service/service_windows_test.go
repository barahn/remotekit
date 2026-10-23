// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package service

import (
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestBuildWindowsServiceConfig_ServiceAccount(t *testing.T) {
	t.Run("default account is empty for LocalSystem", func(t *testing.T) {
		cfg := Config{
			DisplayName: "Test Service",
			Description: "Test Description",
			AutoStart:   true,
		}
		mgrCfg := buildWindowsServiceConfig(cfg)
		if mgrCfg.ServiceStartName != "" {
			t.Errorf("expected empty ServiceStartName, got %q", mgrCfg.ServiceStartName)
		}
		if mgrCfg.Password != "" {
			t.Errorf("expected empty Password, got %q", mgrCfg.Password)
		}
		if mgrCfg.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS {
			t.Errorf("expected SERVICE_WIN32_OWN_PROCESS, got %v", mgrCfg.ServiceType)
		}
		if mgrCfg.StartType != uint32(mgr.StartAutomatic) {
			t.Errorf("expected StartAutomatic, got %v", mgrCfg.StartType)
		}
	})

	t.Run("configured least privilege service account", func(t *testing.T) {
		cfg := Config{
			DisplayName:    "Test Service",
			Description:    "Test Description",
			AutoStart:      false,
			ServiceAccount: `NT AUTHORITY\NetworkService`,
			Password:       "secretPass",
		}
		mgrCfg := buildWindowsServiceConfig(cfg)
		if mgrCfg.ServiceStartName != `NT AUTHORITY\NetworkService` {
			t.Errorf("expected ServiceStartName %q, got %q", `NT AUTHORITY\NetworkService`, mgrCfg.ServiceStartName)
		}
		if mgrCfg.Password != "secretPass" {
			t.Errorf("expected Password %q, got %q", "secretPass", mgrCfg.Password)
		}
		if mgrCfg.StartType != uint32(mgr.StartManual) {
			t.Errorf("expected StartManual, got %v", mgrCfg.StartType)
		}
	})
}
