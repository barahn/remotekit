// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package screen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// TestParseStreamsVariant covers the shape xdg-desktop-portal actually returns.
//
// The portal answers Start with `streams` of signature a(ua{sv}). godbus
// represents a struct as []interface{}, so an array of them arrives as
// [][]interface{}. An earlier type switch matched [][2]interface{} and
// []interface{} instead, matched neither, and left nodeID at zero after a
// successful Start - the capture then failed with "Start returned no PipeWire
// node id" while the portal had done its part.
func TestParseStreamsVariant(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
	}{
		{
			name: "godbus struct array",
			value: [][]interface{}{
				{uint32(42), map[string]dbus.Variant{
					"size": dbus.MakeVariant([]interface{}{int32(1280), int32(800)}),
				}},
			},
		},
		{
			name: "generic interface slice",
			value: []interface{}{
				[]interface{}{uint32(42), map[string]dbus.Variant{}},
			},
		},
		{
			name: "fixed-size pair array",
			value: [][2]interface{}{
				{uint32(42), map[string]dbus.Variant{}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &waylandCapturer{}
			c.parseStreamsVariant(dbus.MakeVariant(tc.value))
			if c.nodeID != 42 {
				t.Errorf("expected node id 42, got %d", c.nodeID)
			}
		})
	}
}

func TestParseStreamsVariantRejectsGarbage(t *testing.T) {
	// nil is absent on purpose: dbus.MakeVariant(nil) panics inside godbus, so
	// a nil streams value can never reach this parser.
	for _, value := range []interface{}{
		"not a stream list",
		[][]interface{}{},
		[][]interface{}{{"missing the node id"}},
	} {
		c := &waylandCapturer{}
		c.parseStreamsVariant(dbus.MakeVariant(value))
		if c.nodeID != 0 {
			t.Errorf("value %#v should not have produced a node id, got %d", value, c.nodeID)
		}
	}
}

// TestFallbackToX11ReportsFailureUnderWayland pins the contract that let a
// broken capture look healthy.
//
// Under Wayland this fallback refuses to grab the X root window — an Xwayland
// grab returns a blank screen rather than failing — and closes the frame
// channel instead. It used to return nothing, so Start returned nil and handed
// the caller a closed channel that read as a working capture. pkg/tunnel checks
// for a nil channel, not a closed one, so the capture_unavailable notice never
// reached the viewer and the operator watched a spinner forever.
func TestFallbackToX11ReportsFailureUnderWayland(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")

	c := &waylandCapturer{
		config: DefaultConfig(),
		frames: make(chan *Frame, 1),
	}

	if established := c.fallbackToX11(context.Background()); established {
		t.Fatal("fallbackToX11 reported a working frame source under Wayland; Start would return nil and the caller would never learn capture is unavailable")
	}

	select {
	case _, open := <-c.frames:
		if open {
			t.Fatal("a frame arrived on a channel that should have been closed")
		}
	default:
		t.Fatal("the frame channel was left open, so a consumer would block forever instead of seeing capture end")
	}
}

func TestEnsureDesktopFile_Content(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	content := "[Desktop Entry]\nName=Chirp\nType=Application\nExec=chirp\n"
	err := ensureDesktopFile("org.barahn.Chirp", content)
	if err != nil {
		t.Fatalf("ensureDesktopFile failed: %v", err)
	}

	dest := filepath.Join(tempDir, "applications", "org.barahn.Chirp.desktop")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read written desktop file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content = %q, want %q", string(got), content)
	}
}

func TestEnsureDesktopFile_Path(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "barahn-custom.desktop")
	content := "[Desktop Entry]\nName=Barahn\nType=Application\n"
	if err := os.WriteFile(srcFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	err := ensureDesktopFile("barahn", srcFile)
	if err != nil {
		t.Fatalf("ensureDesktopFile failed: %v", err)
	}

	dest := filepath.Join(tempDir, "applications", "barahn.desktop")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read copied desktop file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content = %q, want %q", string(got), content)
	}
}

func TestEnsureDesktopFile_NoOpWhenEmpty(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	if err := ensureDesktopFile("", "content"); err != nil {
		t.Errorf("empty appID returned error: %v", err)
	}
	if err := ensureDesktopFile("app", ""); err != nil {
		t.Errorf("empty desktopFile returned error: %v", err)
	}

	// Verify no files were created
	entries, _ := os.ReadDir(filepath.Join(tempDir, "applications"))
	if len(entries) > 0 {
		t.Fatalf("expected no files created, got %d", len(entries))
	}
}

func TestEnsureDesktopFile_NormalizesSuffix(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	content := "[Desktop Entry]\nName=Chirp\n"
	if err := ensureDesktopFile("chirp.desktop", content); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tempDir, "applications", "chirp.desktop")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected file at %s: %v", dest, err)
	}
	badDest := filepath.Join(tempDir, "applications", "chirp.desktop.desktop")
	if _, err := os.Stat(badDest); !os.IsNotExist(err) {
		t.Fatalf("unexpected duplicate suffix file: %s", badDest)
	}
}

func TestSanitizeTokenPrefix(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "barahn"},
		{"com.barahn.agent", "com_barahn_agent"},
		{"org.barahn.Chirp-App", "org_barahn_Chirp_App"},
		{"app123_test", "app123_test"},
		{"invalid!@#$chars", "invalid____chars"},
	}

	for _, tc := range cases {
		got := sanitizeTokenPrefix(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeTokenPrefix(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

type fakeDBusObject struct {
	calledMethod string
	calledArgs   []interface{}
	errToReturn  error
}

func (f *fakeDBusObject) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...interface{}) *dbus.Call {
	f.calledMethod = method
	f.calledArgs = args
	c := &dbus.Call{
		Err: f.errToReturn,
	}
	return c
}

func TestRegisterHostPortal_Success(t *testing.T) {
	fakeObj := &fakeDBusObject{}
	var logged []string
	logf := func(format string, args ...interface{}) {
		logged = append(logged, format)
	}

	err := registerHostPortal(context.Background(), fakeObj, "org.barahn.Chirp", logf)
	if err != nil {
		t.Fatalf("registerHostPortal failed: %v", err)
	}

	if fakeObj.calledMethod != "org.freedesktop.host.portal.Registry.Register" {
		t.Fatalf("calledMethod = %q, want org.freedesktop.host.portal.Registry.Register", fakeObj.calledMethod)
	}
	if len(fakeObj.calledArgs) != 2 || fakeObj.calledArgs[0] != "org.barahn.Chirp" {
		t.Fatalf("calledArgs = %#v, want app_id = %q", fakeObj.calledArgs, "org.barahn.Chirp")
	}
}

func TestRegisterHostPortal_GracefulFallback(t *testing.T) {
	fakeObj := &fakeDBusObject{
		errToReturn: dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"},
	}
	var logged []string
	logf := func(format string, args ...interface{}) {
		logged = append(logged, format)
	}

	// When portal does not support Registry.Register, it should not fail the capture process
	err := registerHostPortal(context.Background(), fakeObj, "org.barahn.Chirp", logf)
	if err != nil {
		t.Fatalf("expected nil error on graceful fallback, got: %v", err)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "continuing") {
		t.Fatalf("expected fallback log message, got: %v", logged)
	}
}

func TestRegisterHostPortal_NoOpWhenEmpty(t *testing.T) {
	fakeObj := &fakeDBusObject{}
	logf := func(format string, args ...interface{}) {}

	err := registerHostPortal(context.Background(), fakeObj, "", logf)
	if err != nil {
		t.Fatal(err)
	}
	if fakeObj.calledMethod != "" {
		t.Fatalf("expected no D-Bus call for empty appID, got: %q", fakeObj.calledMethod)
	}
}

