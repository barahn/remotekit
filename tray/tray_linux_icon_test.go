// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tray must be drawable on a host where nothing has been installed into an
// icon theme, and from a process whose working directory is not the source
// tree. Both are the normal case for an agent started as an OS service.
func TestLoadLinuxIconPixmapsFallsBackToEmbeddedAsset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	pixmaps := loadLinuxIconPixmaps()
	if len(pixmaps) == 0 {
		t.Fatal("expected the embedded asset to yield pixmaps with no installed icon and an unrelated working directory")
	}

	for _, pm := range pixmaps {
		want := int(pm.Width) * int(pm.Height) * 4
		if len(pm.Data) != want {
			t.Errorf("pixmap %dx%d: got %d bytes of ARGB32 data, want %d", pm.Width, pm.Height, len(pm.Data), want)
		}
		var opaque bool
		for i := 0; i < len(pm.Data); i += 4 {
			if pm.Data[i] != 0 {
				opaque = true
				break
			}
		}
		if !opaque {
			t.Errorf("pixmap %dx%d is fully transparent, so nothing would be drawn", pm.Width, pm.Height)
		}
	}
}

// A candidate resolved against the working directory would let the directory a
// binary happens to be launched from decide what the panel displays.
func TestIconFileCandidatesAreAbsolute(t *testing.T) {
	t.Setenv("HOME", "/home/someone")

	candidates := iconFileCandidates()
	if len(candidates) == 0 {
		t.Fatal("expected candidate icon paths")
	}
	for _, p := range candidates {
		if !filepath.IsAbs(p) {
			t.Errorf("candidate %q is relative to the working directory", p)
		}
	}
}

func TestResolveIconName(t *testing.T) {
	tests := []struct {
		name          string
		install       string
		wantIconName  string
		wantThemeTail string
	}{
		{
			name:         "nothing installed advertises no themed icon",
			install:      "",
			wantIconName: "",
		},
		{
			name:          "symbolic png in the hicolor apps directory",
			install:       ".local/share/icons/hicolor/24x24/apps/barahn-symbolic.png",
			wantIconName:  "barahn-symbolic",
			wantThemeTail: ".local/share/icons/hicolor/24x24/apps",
		},
		{
			name:          "symbolic svg in the hicolor apps directory",
			install:       ".local/share/icons/hicolor/24x24/apps/barahn-symbolic.svg",
			wantIconName:  "barahn-symbolic",
			wantThemeTail: ".local/share/icons/hicolor/24x24/apps",
		},
		{
			name:          "colour icon only",
			install:       ".local/share/icons/hicolor/24x24/apps/barahn.png",
			wantIconName:  "barahn",
			wantThemeTail: ".local/share/icons/hicolor/24x24/apps",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			// An empty theme directory must not be mistaken for an installed
			// icon: that is the state that produced an invisible indicator.
			if err := os.MkdirAll(filepath.Join(home, ".local/share/icons/hicolor/24x24/apps"), 0o750); err != nil {
				t.Fatal(err)
			}
			if tc.install != "" {
				full := filepath.Join(home, tc.install)
				if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			iconName, themePath := resolveIconName()
			if iconName != tc.wantIconName {
				t.Errorf("icon name: got %q, want %q", iconName, tc.wantIconName)
			}
			if tc.wantIconName == "" {
				if themePath != "" {
					t.Errorf("theme path: got %q, want empty when no icon is installed", themePath)
				}
				return
			}
			if !strings.HasSuffix(themePath, tc.wantThemeTail) {
				t.Errorf("theme path: got %q, want it to end in %q", themePath, tc.wantThemeTail)
			}
			if _, err := os.Stat(themePath); err != nil {
				t.Errorf("advertised theme path does not exist: %v", err)
			}
		})
	}
}
