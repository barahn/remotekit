// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build windows

// Package pathtrust refuses to let a privileged process consume configuration
// from locations that an unprivileged local user can modify.
package pathtrust

import "os"

func privileged() bool { return false }

// VerifyPath is a no-op on Windows, where POSIX ownership checks do not apply.
func VerifyPath(path string) error { return nil }

// ReadFile reads path like os.ReadFile on Windows.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- caller validates path
}
