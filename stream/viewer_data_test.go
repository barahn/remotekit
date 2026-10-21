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
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
)

// dataSession runs a Runner, with an in-memory clipboard, and a Viewer against
// each other, and waits until the viewer's data channels are open.
func dataSession(t *testing.T, permissions ...string) (*Runner, *Viewer, *clipboard.MemoryClipboard) {
	t.Helper()
	clip := clipboard.NewMemoryClipboard()
	r := New(Config{ID: "agent-1"})
	r.clip = clip
	r.RequireDataChannel(true)
	r.Grant(permissions...)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	session(t, r, v)

	waitChannels(t, v)
	return r, v, clip
}

// channelsOpen reports whether both data channels of the current peer are open.
func (v *Viewer) channelsOpen() bool {
	v.mu.Lock()
	peer := v.peer
	v.mu.Unlock()
	return peer != nil && peer.DataChannelOpen("bark-control") && peer.DataChannelOpen("bark-transfer")
}

func TestViewerClipboardBothWays(t *testing.T) {
	_, v, clip := dataSession(t, PermissionClipboard)
	got := make(chan string, 4)
	v.OnClipboard(func(text string) { got <- text })

	// Viewer to agent.
	if err := v.SendClipboard("from the viewer"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		text, _ := clip.GetText(context.Background())
		if text == "from the viewer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent clipboard = %q, want the viewer's text", text)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Agent to viewer: the agent's watcher sees a change and sends it over
	// the control channel.
	_ = clip.SetText(context.Background(), "from the agent")
	select {
	case text := <-got:
		if text != "from the agent" {
			t.Fatalf("viewer got %q, want the agent's clipboard", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent's clipboard never reached the viewer")
	}
}

func TestViewerClipboardNeedsConsent(t *testing.T) {
	_, v, clip := dataSession(t) // nothing granted
	if err := v.SendClipboard("should not land"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if text, _ := clip.GetText(context.Background()); text != "" {
		t.Fatalf("agent clipboard = %q without clipboard consent", text)
	}
}

func TestViewerSendsAFile(t *testing.T) {
	_, v, _ := dataSession(t, PermissionFileTransfer)

	// Several windows' worth, ending in a partial chunk.
	content := bytes.Repeat([]byte("remotekit "), (viewerChunkSize*fileWindow*2+777)/10)
	sum := sha256.Sum256(content)
	name := "viewer-send-" + hex.EncodeToString(sum[:4]) + ".bin"

	var last, total int64
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	saved, err := v.SendFile(ctx, "../../"+name, bytes.NewReader(content), func(sent, size int64) { last, total = sent, size })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(saved.Path) })

	if filepath.Base(saved.Path) != name {
		t.Errorf("saved as %q, want the bare name %q: directories must be dropped", saved.Path, name)
	}
	if saved.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %s, want %x", saved.SHA256, sum)
	}
	if last != int64(len(content)) || total != int64(len(content)) {
		t.Errorf("last progress = %d of %d, want %d of %d", last, total, len(content), len(content))
	}
	onDisk, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, content) {
		t.Fatal("the file on the agent's disk differs from what was sent")
	}
}

func TestViewerSendFileIsRefusedWithoutConsent(t *testing.T) {
	_, v, _ := dataSession(t) // nothing granted
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := v.SendFile(ctx, "refused.bin", bytes.NewReader([]byte("nope")), nil)
	if !errors.Is(err, ErrFileNotSaved) {
		t.Fatalf("want ErrFileNotSaved, got %v", err)
	}
}

func TestViewerSendFileNeedsAConnection(t *testing.T) {
	v := NewViewer(ViewerConfig{ID: "v", AgentID: "a"})
	if _, err := v.SendFile(context.Background(), "x", bytes.NewReader(nil), nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendFile before Serve: want ErrNotConnected, got %v", err)
	}
	if err := v.SendClipboard("x"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendClipboard before Serve: want ErrNotConnected, got %v", err)
	}
}
