// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package osinfo

import (
	"os"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name: "Ubuntu 24.04",
			input: `NAME="Ubuntu"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
PRETTY_NAME="Ubuntu 24.04.1 LTS"
VERSION_ID="24.04"
`,
			expected: map[string]string{
				"NAME":        "Ubuntu",
				"ID":          "ubuntu",
				"ID_LIKE":     "debian",
				"PRETTY_NAME": "Ubuntu 24.04.1 LTS",
				"VERSION_ID":  "24.04",
			},
		},
		{
			name: "Fedora 44",
			input: `NAME="Fedora Linux"
VERSION="44 (Container Image)"
ID=fedora
VERSION_ID=44
PRETTY_NAME="Fedora Linux 44 (Container Image)"
`,
			expected: map[string]string{
				"NAME":        "Fedora Linux",
				"ID":          "fedora",
				"VERSION_ID":  "44",
				"PRETTY_NAME": "Fedora Linux 44 (Container Image)",
			},
		},
		{
			name: "Xubuntu",
			input: `NAME="Xubuntu"
VERSION="24.04 LTS"
ID=xubuntu
ID_LIKE=ubuntu
PRETTY_NAME="Xubuntu 24.04 LTS"
VERSION_ID="24.04"
`,
			expected: map[string]string{
				"NAME":        "Xubuntu",
				"ID":          "xubuntu",
				"ID_LIKE":     "ubuntu",
				"PRETTY_NAME": "Xubuntu 24.04 LTS",
				"VERSION_ID":  "24.04",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ParseOSRelease(tt.input)
			for k, v := range tt.expected {
				if res[k] != v {
					t.Errorf("key %s: expected %q, got %q", k, v, res[k])
				}
			}
		})
	}
}

func TestMapLinuxIconKey(t *testing.T) {
	tests := []struct {
		id       string
		idLike   string
		expected string
	}{
		{"xubuntu", "ubuntu", "xubuntu-linux"},
		{"ubuntu", "debian", "ubuntu-linux"},
		{"fedora", "rhel", "fedora"},
		{"debian", "", "debian-linux"},
		{"arch", "", "arch-linux"},
		{"alpine", "", "alpine-linux"},
		{"linuxmint", "ubuntu", "linux-mint"},
		{"kali", "debian", "kali-linux"},
		{"unknown", "fedora", "fedora"},
		{"unknown", "", "tux"},
	}

	for _, tt := range tests {
		res := MapLinuxIconKey(tt.id, tt.idLike)
		if res != tt.expected {
			t.Errorf("MapLinuxIconKey(%q, %q): expected %q, got %q", tt.id, tt.idLike, tt.expected, res)
		}
	}
}

func TestDetectDisplayServer(t *testing.T) {
	origSession := os.Getenv("XDG_SESSION_TYPE")
	origWayland := os.Getenv("WAYLAND_DISPLAY")
	origDisplay := os.Getenv("DISPLAY")
	defer func() {
		_ = os.Setenv("XDG_SESSION_TYPE", origSession)
		_ = os.Setenv("WAYLAND_DISPLAY", origWayland)
		_ = os.Setenv("DISPLAY", origDisplay)
	}()

	// Test Wayland via XDG_SESSION_TYPE
	_ = os.Setenv("XDG_SESSION_TYPE", "wayland")
	_ = os.Setenv("WAYLAND_DISPLAY", "")
	_ = os.Setenv("DISPLAY", "")
	if ds := detectLinuxDisplayServer(); ds != "wayland" {
		t.Errorf("expected 'wayland', got %q", ds)
	}

	// Test Wayland via WAYLAND_DISPLAY
	_ = os.Setenv("XDG_SESSION_TYPE", "")
	_ = os.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if ds := detectLinuxDisplayServer(); ds != "wayland" {
		t.Errorf("expected 'wayland' via WAYLAND_DISPLAY, got %q", ds)
	}

	// Test X11 via DISPLAY
	_ = os.Setenv("XDG_SESSION_TYPE", "x11")
	_ = os.Setenv("WAYLAND_DISPLAY", "")
	_ = os.Setenv("DISPLAY", ":99")
	if ds := detectLinuxDisplayServer(); ds != "x11" {
		t.Errorf("expected 'x11', got %q", ds)
	}
}

func TestDetect(t *testing.T) {
	info := Detect()
	if info.OS == "" {
		t.Errorf("Detect returned empty OS")
	}
	if info.Formatted == "" {
		t.Errorf("Detect returned empty Formatted string")
	}
	if info.IconKey == "" {
		t.Errorf("Detect returned empty IconKey")
	}
}
