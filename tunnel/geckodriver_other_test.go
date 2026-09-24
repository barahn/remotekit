// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !unix

package tunnel

import "os/exec"

func isolateProcessGroup(*exec.Cmd) {}

func killDriver(int) {}
