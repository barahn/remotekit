// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
)

// dataSessionWith is dataSession with setup run on both ends before either
// serves, and the host clipboard's content before the session.
func dataSessionWith(t *testing.T, before string, setup func(*Runner, *Viewer), permissions ...string) (*Runner, *Viewer, *clipboard.MemoryClipboard) {
	t.Helper()
	clip := clipboard.NewMemoryClipboard()
	_ = clip.SetText(context.Background(), before)
	r := New(Config{ID: "agent-1"})
	r.clip = clip
	r.RequireDataChannel(true)
	r.Grant(permissions...)
	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	if setup != nil {
		setup(r, v)
	}
	session(t, r, v)
	waitChannels(t, v)
	return r, v, clip
}

// clipWatch collects both ends' clipboard traffic.
type clipWatch struct {
	events chan ClipboardEvent
	viewer chan string
}

func watchClipboard() (*clipWatch, func(*Runner, *Viewer)) {
	w := &clipWatch{events: make(chan ClipboardEvent, 8), viewer: make(chan string, 8)}
	return w, func(r *Runner, v *Viewer) {
		r.OnClipboardEvent(func(ev ClipboardEvent) { w.events <- ev })
		v.OnClipboard(func(text string) { w.viewer <- text })
	}
}

func (w *clipWatch) event(t *testing.T) ClipboardEvent {
	t.Helper()
	select {
	case ev := <-w.events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no clipboard event within 5s")
	}
	return ClipboardEvent{}
}

// quietFor fails if the viewer is sent anything for d: three watch periods.
func (w *clipWatch) quietFor(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case text := <-w.viewer:
		t.Fatalf("the viewer was sent %q", text)
	case <-time.After(d):
	}
}

// Every paste a viewer makes into the host, and every copy the host makes
// that goes to the viewers, is reported -- with its size, never its text.
func TestClipboardEventsReportCopyAndPaste(t *testing.T) {
	w, setup := watchClipboard()
	_, v, clip := dataSessionWith(t, "", setup, PermissionClipboard)

	if err := v.SendClipboard("abc"); err != nil {
		t.Fatal(err)
	}
	if ev := w.event(t); ev.Direction != ClipboardToHost || ev.ViewerID != "viewer-1" || ev.Size != 3 || ev.Concealed {
		t.Fatalf("paste reported as %+v", ev)
	}

	_ = clip.SetText(context.Background(), "copied on the host")
	if ev := w.event(t); ev.Direction != ClipboardFromHost || ev.Size != len("copied on the host") || ev.Concealed {
		t.Fatalf("copy reported as %+v", ev)
	}
	select {
	case text := <-w.viewer:
		if text != "copied on the host" {
			t.Fatalf("the viewer got %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host's copy did not reach the viewer")
	}
}

// What a password manager copies never leaves the host; the consumer hears
// that something was held back.
func TestConcealedClipboardIsNeverSent(t *testing.T) {
	w, setup := watchClipboard()
	_, _, clip := dataSessionWith(t, "", setup, PermissionClipboard)

	_ = clip.SetConcealedText(context.Background(), "hunter2")
	if ev := w.event(t); ev.Direction != ClipboardFromHost || !ev.Concealed {
		t.Fatalf("a concealed copy reported as %+v", ev)
	}
	w.quietFor(t, 1500*time.Millisecond)
}

// What was copied before the clipboard was granted is never sent, however
// long the session ran before; the first copy after is.
func TestTheRunnerIgnoresWhatWasCopiedBeforeTheGrant(t *testing.T) {
	w, setup := watchClipboard()
	r, _, clip := dataSessionWith(t, "copied before the session", setup)
	_ = clip.SetText(context.Background(), "copied before the grant")
	time.Sleep(700 * time.Millisecond) // a watch period without the grant

	r.Grant(PermissionClipboard)
	w.quietFor(t, 1500*time.Millisecond)

	_ = clip.SetText(context.Background(), "copied after the grant")
	select {
	case text := <-w.viewer:
		if text != "copied after the grant" {
			t.Fatalf("the viewer got %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the copy made after the grant did not reach the viewer")
	}
}
