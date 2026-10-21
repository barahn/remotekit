// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package macos binds the macOS system frameworks that screen, input and
// permission call, through github.com/ebitengine/purego rather than CGo, so
// remotekit keeps building with CGO_ENABLED=0 and cross-compiling from any
// host. Everything in it is darwin-only; on other platforms it is empty.
package macos
