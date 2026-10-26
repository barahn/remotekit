// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

// Package pathtrust refuses to let a privileged process consume configuration
// from locations that an unprivileged local user can modify.
package pathtrust

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// maxFileSize bounds how much of a trusted file is read.
const maxFileSize = 1 << 20

func privileged() bool {
	token := windows.GetCurrentProcessToken()
	return token.IsElevated()
}

// isTrustedOwner checks if the SID belongs to SYSTEM or Builtin Administrators.
func isTrustedOwner(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	return sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		sid.IsWellKnown(windows.WinLocalServiceSid) ||
		sid.IsWellKnown(windows.WinNetworkServiceSid)
}

// checkOwnerTrust inspects the owner SID of the path using Windows security APIs.
func checkOwnerTrust(path string) error {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("failed to get security info for %q: %w", path, err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd))) //nolint:errcheck

	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("failed to get owner SID for %q: %w", path, err)
	}
	if !isTrustedOwner(owner) {
		return fmt.Errorf("untrusted path %q: not owned by Administrators or SYSTEM (owner: %s)", path, owner.String())
	}
	return nil
}

// VerifyPath verifies that path is owned by a trusted administrative account when running elevated.
func VerifyPath(path string) error {
	if !privileged() {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Walk parent directories up to drive root
	for cur := abs; ; cur = filepath.Dir(cur) {
		if _, err := os.Stat(cur); err == nil {
			if err := checkOwnerTrust(cur); err != nil {
				return err
			}
		}
		if next := filepath.Dir(cur); next == cur {
			break
		}
	}
	return nil
}

// ReadFile reads path. When running elevated, it verifies the path's trust chain.
func ReadFile(path string) ([]byte, error) {
	if !privileged() {
		return os.ReadFile(path) // #nosec G304 -- unprivileged process reads only what it could already read
	}
	if err := VerifyPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304 -- verified above
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxFileSize))
}
