// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentFileSession runs a Runner and a Viewer against each other with
// setup applied to both before either serves, the viewer saving into a
// fresh folder, and waits for the data channels.
func agentFileSession(t *testing.T, setup func(*Runner, *Viewer), permissions ...string) (*Runner, *Viewer, string) {
	t.Helper()
	dir := t.TempDir()
	r := New(Config{ID: "agent-1"})
	r.RequireDataChannel(true)
	r.Grant(permissions...)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1", DownloadDir: dir})
	if setup != nil {
		setup(r, v)
	}
	session(t, r, v)
	waitChannels(t, v)
	return r, v, dir
}

func acceptFiles(offers *[]FileOffer, saved chan<- ReceivedFile) func(*Runner, *Viewer) {
	return func(_ *Runner, v *Viewer) {
		v.OnFileOffer(func(o FileOffer) bool { *offers = append(*offers, o); return true })
		v.OnFileSaved(func(f ReceivedFile) { saved <- f })
	}
}

// The agent sends a file to a viewer that accepts it: it lands whole in the
// viewer's folder, the viewer hears of it, and the agent learns where.
func TestAgentSendsAFileToTheViewer(t *testing.T) {
	var offers []FileOffer
	saved := make(chan ReceivedFile, 1)
	r, _, dir := agentFileSession(t, acceptFiles(&offers, saved), PermissionFileTransfer)

	content := bytes.Repeat([]byte("log line\n"), 20000) // several chunks
	sum := sha256.Sum256(content)
	var lastSent, total int64
	got, err := r.SendFile(context.Background(), "viewer-1", "agent.log", bytes.NewReader(content), func(sent, all int64) { lastSent, total = sent, all })
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != hex.EncodeToString(sum[:]) || filepath.Dir(got.Path) != dir {
		t.Fatalf("SendFile = %+v; want the file's hash, in %s", got, dir)
	}
	if data, err := os.ReadFile(got.Path); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("the saved file differs (%v)", err)
	}
	if lastSent != int64(len(content)) || total != int64(len(content)) {
		t.Errorf("progress ended at %d of %d, want %d", lastSent, total, len(content))
	}
	if len(offers) != 1 || offers[0].Name != "agent.log" || offers[0].Size != int64(len(content)) {
		t.Errorf("offers %+v", offers)
	}
	select {
	case f := <-saved:
		if f.Path != got.Path || f.Name != "agent.log" {
			t.Errorf("OnFileSaved got %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Error("the viewer was not told the file was saved")
	}
}

// The person at the machine decides whether files move at all.
func TestAgentFilesNeedFileTransfer(t *testing.T) {
	var offers []FileOffer
	saved := make(chan ReceivedFile, 1)
	r, _, _ := agentFileSession(t, acceptFiles(&offers, saved))
	_, err := r.SendFile(context.Background(), "viewer-1", "x.txt", strings.NewReader("x"), nil)
	if !errors.Is(err, ErrNotGranted) {
		t.Fatalf("SendFile without file_transfer: %v, want ErrNotGranted", err)
	}
	if len(offers) != 0 {
		t.Fatalf("the viewer was offered %+v", offers)
	}
}

// A viewer takes files only when its consumer asks to decide on them.
func TestTheViewerRefusesFilesUnlessItAsks(t *testing.T) {
	r, _, dir := agentFileSession(t, nil, PermissionFileTransfer)
	_, err := r.SendFile(context.Background(), "viewer-1", "x.txt", strings.NewReader("x"), nil)
	if !errors.Is(err, ErrFileNotSaved) {
		t.Fatalf("SendFile to a viewer without OnFileOffer: %v, want ErrFileNotSaved", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the viewer's folder holds %v", entries)
	}
}

func TestTheViewerCanRefuseAFile(t *testing.T) {
	r, _, _ := agentFileSession(t, func(_ *Runner, v *Viewer) {
		v.OnFileOffer(func(FileOffer) bool { return false })
	}, PermissionFileTransfer)
	_, err := r.SendFile(context.Background(), "viewer-1", "x.txt", strings.NewReader("x"), nil)
	if !errors.Is(err, ErrFileNotSaved) {
		t.Fatalf("a refused file: %v, want ErrFileNotSaved", err)
	}
}

// Files go only over a viewer's data channel: to a viewer that is not there,
// SendFile fails rather than use the signalling path.
func TestAgentFilesNeedTheViewersDataChannel(t *testing.T) {
	r, _, _ := agentFileSession(t, nil, PermissionFileTransfer)
	_, err := r.SendFile(context.Background(), "nobody", "x.txt", strings.NewReader("x"), nil)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendFile to an unknown viewer: %v, want ErrNotConnected", err)
	}
}

// Taking file transfer back stops a file on its way out at the next chunk.
func TestRevokingFileTransferStopsASendUnderWay(t *testing.T) {
	var offers []FileOffer
	saved := make(chan ReceivedFile, 1)
	r, _, _ := agentFileSession(t, acceptFiles(&offers, saved), PermissionFileTransfer)
	content := bytes.Repeat([]byte{7}, viewerChunkSize*(fileWindow+8))
	revoked := false
	_, err := r.SendFile(context.Background(), "viewer-1", "big.bin", bytes.NewReader(content), func(sent, total int64) {
		if !revoked {
			revoked = true
			r.Revoke(PermissionFileTransfer)
		}
	})
	if !errors.Is(err, ErrNotGranted) {
		t.Fatalf("SendFile after the permission was taken back: %v, want ErrNotGranted", err)
	}
	select {
	case f := <-saved:
		t.Fatalf("the viewer saved %+v after the permission was taken back", f)
	case <-time.After(300 * time.Millisecond):
	}
}
