// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/barahn/remotekit/bark"
)

// ScriptRunner executes ad-hoc commands and scripts in the background on the agent OS.
type ScriptRunner struct{}

func NewScriptRunner() *ScriptRunner {
	return &ScriptRunner{}
}

// Execute runs a script execution request, streaming output chunks and returning final result.
func (sr *ScriptRunner) Execute(ctx context.Context, req bark.ScriptExecutionRequest, onChunk func(chunk bark.ScriptExecutionChunk)) bark.ScriptExecutionResult {
	start := time.Now().UTC()

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 60 // Default 60s timeout
	} else if timeoutSec > 600 {
		timeoutSec = 600 // Maximum 10m timeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd

	switch req.Interpreter {
	case "powershell", "pwsh":
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", req.ScriptBody) // #nosec G204
		} else {
			cmd = exec.CommandContext(execCtx, "pwsh", "-NoProfile", "-NonInteractive", "-Command", req.ScriptBody) // #nosec G204
		}
	case "cmd":
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "cmd.exe", "/c", req.ScriptBody) // #nosec G204
		} else {
			cmd = exec.CommandContext(execCtx, "/bin/sh", "-c", req.ScriptBody) // #nosec G204
		}
	case "bash":
		cmd = exec.CommandContext(execCtx, "/bin/bash", "-c", req.ScriptBody) // #nosec G204
	default: // "sh" / auto
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", req.ScriptBody) // #nosec G204
		} else {
			cmd = exec.CommandContext(execCtx, "/bin/sh", "-c", req.ScriptBody) // #nosec G204
		}
	}

	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return bark.ScriptExecutionResult{
			ExecutionID:         req.ExecutionID,
			ExitCode:            -1,
			ExecutionDurationMS: time.Since(start).Milliseconds(),
			Error:               fmt.Sprintf("failed to get stdout pipe: %v", err),
		}
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return bark.ScriptExecutionResult{
			ExecutionID:         req.ExecutionID,
			ExitCode:            -1,
			ExecutionDurationMS: time.Since(start).Milliseconds(),
			Error:               fmt.Sprintf("failed to get stderr pipe: %v", err),
		}
	}

	if err := cmd.Start(); err != nil {
		return bark.ScriptExecutionResult{
			ExecutionID:         req.ExecutionID,
			ExitCode:            -1,
			ExecutionDurationMS: time.Since(start).Milliseconds(),
			Error:               fmt.Sprintf("failed to start process: %v", err),
		}
	}

	var wg sync.WaitGroup
	var chunkMu sync.Mutex
	chunkIndex := 0

	streamReader := func(r io.Reader, streamName string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			chunkMu.Lock()
			chunkIndex++
			chunk := bark.ScriptExecutionChunk{
				ExecutionID: req.ExecutionID,
				Stream:      streamName,
				Data:        line,
				Index:       chunkIndex,
			}
			chunkMu.Unlock()
			if onChunk != nil {
				onChunk(chunk)
			}
		}
	}

	wg.Add(2)
	go streamReader(stdoutPipe, "stdout")
	go streamReader(stderrPipe, "stderr")

	wg.Wait()
	waitErr := cmd.Wait()

	duration := time.Since(start).Milliseconds()
	exitCode := 0
	errStr := ""

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		errStr = waitErr.Error()
	}

	return bark.ScriptExecutionResult{
		ExecutionID:         req.ExecutionID,
		ExitCode:            exitCode,
		ExecutionDurationMS: duration,
		Error:               errStr,
	}
}
