// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package service

import (
	"strings"
	"testing"
)

func TestBuildSystemdUnit_HardeningDirectives(t *testing.T) {
	cfg := Config{
		Description: "Barahn Agent Daemon",
		WorkDir:     "/etc/barahn",
		Args:        []string{"agent", "start"},
	}
	unit := BuildSystemdUnit(cfg, "/usr/local/bin/barahn-agent")

	requiredDirectives := []string{
		"ProtectSystem=full",
		"ProtectHome=read-only",
		"NoNewPrivileges=true",
		"PrivateTmp=true",
	}

	for _, dir := range requiredDirectives {
		if !strings.Contains(unit, dir) {
			t.Errorf("BuildSystemdUnit() missing hardening directive: %q\nUnit content:\n%s", dir, unit)
		}
	}
}

func TestBuildSystemdUnit_ServiceAccount(t *testing.T) {
	cfg := Config{
		Description:    "Barahn Agent Daemon",
		WorkDir:        "/var/lib/barahn",
		ServiceAccount: "barahn-svc",
	}
	unit := BuildSystemdUnit(cfg, "/usr/local/bin/barahn-agent")

	if !strings.Contains(unit, "User=barahn-svc\n") {
		t.Errorf("BuildSystemdUnit() missing User directive\nUnit content:\n%s", unit)
	}
	if !strings.Contains(unit, "Group=barahn-svc\n") {
		t.Errorf("BuildSystemdUnit() missing Group directive\nUnit content:\n%s", unit)
	}
}

