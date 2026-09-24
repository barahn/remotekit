// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build unix

package tunnel

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts geckodriver in its own process group, so stopping it
// reaches whatever it started rather than only the process this test launched.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killDriver stops geckodriver and the group it leads.
//
// With a snap-installed Firefox this does not always finish the job: the
// geckodriver on PATH is a wrapper, the real one is re-launched inside snap's
// confinement, and signals from outside that confinement are denied -- by
// AppArmor, not by file permissions, so running as the same user does not help.
// An idle geckodriver can therefore outlive the test. Deleting the WebDriver
// session first, which the caller does, is what actually closes Firefox; the
// stray driver holds nothing but a loopback port.
func killDriver(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
