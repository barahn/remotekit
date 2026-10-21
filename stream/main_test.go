// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	code := m.Run()
	if code != 0 {
		os.Exit(code)
	}

	// Pion and transport goroutines may take a moment to unwind after Close().
	// Poll for them to exit within a reasonable deadline.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= initialGoroutines {
			os.Exit(0)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// If extra goroutines linger, check if they are from remotekit or pion.
	buf := new(strings.Builder)
	_ = pprof.Lookup("goroutine").WriteTo(buf, 2)
	stacks := buf.String()

	// Filter out testing/runtime goroutines
	var leaked []string
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "stream.TestMain") || strings.Contains(stack, "_testmain.go") {
			continue
		}
		if strings.Contains(stack, "github.com/barahn/remotekit") ||
			strings.Contains(stack, "github.com/pion") {
			leaked = append(leaked, stack)
		}
	}

	if len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "stream: %d goroutine(s) leaked after all tests completed:\n%s\n",
			len(leaked), strings.Join(leaked, "\n\n"))
		os.Exit(1)
	}

	os.Exit(0)
}
