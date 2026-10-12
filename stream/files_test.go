// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// filesSession serves a Runner configured by setup against a Viewer, and waits
// for the viewer's data channels.
func filesSession(t *testing.T, cfg Config, setup func(*Runner)) *Viewer {
	t.Helper()
	cfg.ID = "agent-1"
	r := New(cfg)
	r.Grant(PermissionFileTransfer)
	if setup != nil {
		setup(r)
	}
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	session(t, r, v)
	deadline := time.Now().Add(20 * time.Second)
	for !v.channelsOpen() {
		if time.Now().After(deadline) {
			t.Fatal("the viewer's data channels did not open within 20s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return v
}

func send(t *testing.T, v *Viewer, name string, content []byte) (SavedFile, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return v.SendFile(ctx, name, bytes.NewReader(content), nil)
}

func TestReceivedFilesLandInDownloadDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "incoming") // created on demand
	saved := make(chan ReceivedFile, 2)
	v := filesSession(t, Config{DownloadDir: dir}, func(r *Runner) {
		r.OnFileSaved(func(f ReceivedFile) { saved <- f })
	})
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the owner's own"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := send(t, v, "notes.txt", []byte("from the viewer"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got.Path) != dir || filepath.Base(got.Path) != "notes (1).txt" {
		t.Fatalf("saved at %q, want %q beside the existing file", got.Path, filepath.Join(dir, "notes (1).txt"))
	}
	if mine, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(mine) != "the owner's own" {
		t.Fatalf("an existing file was overwritten: %q", mine)
	}
	select {
	case f := <-saved:
		if f.ViewerID != "viewer-1" || f.Name != "notes.txt" || f.Path != got.Path || f.SHA256 != got.SHA256 || f.Size != int64(len("from the viewer")) {
			t.Fatalf("OnFileSaved got %+v, want the viewer, name, path, hash and size", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnFileSaved was not called")
	}
}

func TestOnFileOfferRefusesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	offers := make(chan FileOffer, 2)
	v := filesSession(t, Config{DownloadDir: dir}, func(r *Runner) {
		r.OnFileOffer(func(o FileOffer) bool {
			offers <- o
			return filepath.Ext(o.Name) != ".exe"
		})
	})

	_, err := send(t, v, "../setup.exe", bytes.Repeat([]byte{1}, 3*viewerChunkSize))
	if !errors.Is(err, ErrFileNotSaved) {
		t.Fatalf("refused offer: want ErrFileNotSaved, got %v", err)
	}
	o := <-offers
	if o.ViewerID != "viewer-1" || o.Name != "setup.exe" || o.Size != int64(3*viewerChunkSize) || o.TransferID == "" {
		t.Fatalf("offer = %+v, want the viewer, the bare name and the size", o)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused file reached the disk: %v", entries)
	}

	if _, err := send(t, v, "fine.txt", []byte("ok")); err != nil {
		t.Fatalf("accepted offer: %v", err)
	}
}

func TestUnusableDownloadDirRefusesFiles(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v := filesSession(t, Config{DownloadDir: filepath.Join(blocker, "sub")}, nil)
	if _, err := send(t, v, "x.txt", []byte("x")); !errors.Is(err, ErrFileNotSaved) {
		t.Fatalf("want ErrFileNotSaved when the agent cannot store files, got %v", err)
	}
}
