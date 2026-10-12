// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/webrtc"
)

// An empty clipboard is a clipboard: sending it clears the agent's.
func TestViewerClearsTheClipboard(t *testing.T) {
	_, v, clip := dataSession(t, PermissionClipboard)
	_ = clip.SetText(context.Background(), "secret")
	if err := v.SendClipboard(""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if text, _ := clip.GetText(context.Background()); text == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent's clipboard was not cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SendFile sends r from where it stands, and a nameless file gets the
// agent's fallback name.
func TestSendFileFromOffsetAndWithoutName(t *testing.T) {
	dir := t.TempDir()
	v := filesSession(t, Config{DownloadDir: dir}, nil)
	r := bytes.NewReader([]byte("headerBODY"))
	if _, err := r.Seek(6, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	saved, err := v.SendFile(ctx, "", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(saved.Path) != "download.bin" {
		t.Errorf("saved as %q, want the fallback download.bin", saved.Path)
	}
	got, _ := readFile(saved.Path)
	if got != "BODY" {
		t.Errorf("saved %q, want only what followed the reader's position", got)
	}
}

// Under signed viewers, a file offer carries the key whose offer set up the
// channel: the identity to decide by, unlike the viewer_id label.
func TestFileOfferCarriesTheSignedKey(t *testing.T) {
	pub, priv := viewerKey(t)
	offers := make(chan FileOffer, 1)
	r := New(Config{ID: "agent-1", DownloadDir: t.TempDir()})
	r.Grant(PermissionFileTransfer)
	r.RequireSignedViewers(trustOnly(pub))
	r.OnFileOffer(func(o FileOffer) bool { offers <- o; return true })
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1", SigningKey: priv})
	session(t, r, v)
	waitChannels(t, v)
	if _, err := send(t, v, "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if o := <-offers; !bytes.Equal(o.Key, pub) {
		t.Fatalf("offer key = %x, want the viewer's signed key", o.Key)
	}
}

// Asking for the monitor already streamed costs nothing, and switching
// faster than minDisplaySwitch is refused.
func TestDisplaySwitchesAreLimited(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.newCapturer = func(screen.CaptureConfig) (screen.Capturer, error) { return &fakeCapturer{}, nil }
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	lists := make(chan bark.DisplaysPayload, 16)
	v.OnDisplays(func(p bark.DisplaysPayload) { lists <- p })
	session(t, r, v)

	wait := func(ok func(bark.DisplaysPayload) bool) bark.DisplaysPayload {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case p := <-lists:
				if ok(p) {
					return p
				}
			case <-deadline:
				t.Fatal("no matching displays message within 20s")
			}
		}
	}
	wait(func(p bark.DisplaysPayload) bool { return p.Active == 0 })

	if err := v.SelectDisplay(0); err != nil { // already active
		t.Fatal(err)
	}
	if p := wait(func(bark.DisplaysPayload) bool { return true }); p.Error != "" {
		t.Fatalf("selecting the active monitor failed: %s", p.Error)
	}
	if err := v.SelectDisplay(1); err != nil {
		t.Fatal(err)
	}
	if err := v.SelectDisplay(0); err != nil {
		t.Fatal(err)
	}
	p := wait(func(p bark.DisplaysPayload) bool { return p.Error != "" })
	if !strings.Contains(p.Error, "too recently") {
		t.Fatalf("a second switch at once: error %q, want it refused as too soon", p.Error)
	}
}

// A disconnected notice for an offer the viewer has replaced since does not
// close the replacement.
func TestStaleDisconnectKeepsTheNewPeer(t *testing.T) {
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	peer, err := webrtc.NewPeerSession(webrtc.PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	notices := make(chan Notice, 2)
	v.OnNotice(func(n Notice) { notices <- n })
	v.mu.Lock()
	v.peer, v.offerID = peer, "2"
	v.mu.Unlock()

	v.handle([]byte(`{"type":"disconnected","viewer_id":"viewer-1","offer_id":"1"}`))
	if v.peer != peer || len(notices) != 0 {
		t.Fatal("a notice for offer 1 closed the peer of offer 2")
	}
	v.handle([]byte(`{"type":"disconnected","viewer_id":"viewer-1","offer_id":"2"}`))
	if v.peer != nil || len(notices) != 1 {
		t.Fatal("a notice for the current offer did not close its peer")
	}
}
