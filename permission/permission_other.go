// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !darwin

package permission

// Check reports NotRequired: this platform has no such permission.
func Check(Permission) Status { return NotRequired }

// Request reports NotRequired: this platform has no such permission.
func Request(Permission) Status { return NotRequired }
