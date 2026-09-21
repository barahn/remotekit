// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !windows && !linux && !darwin

package clipboard

// NewManager returns a memory fallback clipboard manager on non-supported platforms.
func NewManager() Manager {
	return NewMemoryClipboard()
}
