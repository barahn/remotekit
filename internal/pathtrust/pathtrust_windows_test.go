// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

package pathtrust

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsTrustedOwner(t *testing.T) {
	if isTrustedOwner(nil) {
		t.Error("isTrustedOwner(nil) = true, want false")
	}

	systemSid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("failed to create system SID: %v", err)
	}
	if !isTrustedOwner(systemSid) {
		t.Errorf("isTrustedOwner(SYSTEM) = false, want true")
	}

	adminSid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("failed to create admin SID: %v", err)
	}
	if !isTrustedOwner(adminSid) {
		t.Errorf("isTrustedOwner(Administrators) = false, want true")
	}

	userSid, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatalf("failed to create users SID: %v", err)
	}
	if isTrustedOwner(userSid) {
		t.Errorf("isTrustedOwner(Users) = true, want false")
	}
}

func TestVerifyPath_WindowsUnprivileged(t *testing.T) {
	if privileged() {
		t.Skip("skipping unprivileged test when elevated")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.pem")
	if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPath(p); err != nil {
		t.Errorf("VerifyPath unprivileged returned error: %v", err)
	}
}
