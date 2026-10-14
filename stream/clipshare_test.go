// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"errors"
	"testing"
	"time"
)

// noWatch turns the watch off and hands the viewer's clipboard to got, set up
// before either end serves.
func noWatch(got chan<- string) func(*Runner, *Viewer) {
	return func(r *Runner, v *Viewer) {
		r.DisableClipboardWatch(true)
		v.OnClipboard(func(text string) { got <- text })
	}
}

// With the watch off, nothing the person at the machine copies reaches a
// viewer on its own -- and the viewer's own text is still applied.
func TestDisableClipboardWatchSendsNothingOnItsOwn(t *testing.T) {
	got := make(chan string, 4)
	_, v, clip := dataSessionWith(t, noWatch(got), PermissionClipboard)

	_ = clip.SetText(context.Background(), "a password the owner copied")
	select {
	case text := <-got:
		t.Fatalf("the viewer was sent %q with the watch off", text)
	case <-time.After(1500 * time.Millisecond): // three watch periods
	}

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
			t.Fatalf("host clipboard = %q, want the viewer's text applied with the watch off", text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SendClipboard sends exactly what it is given, once, without reading the
// host's clipboard.
func TestSendClipboardReachesTheViewer(t *testing.T) {
	got := make(chan string, 4)
	r, _, clip := dataSessionWith(t, noWatch(got), PermissionClipboard)
	_ = clip.SetText(context.Background(), "never read")

	if err := r.SendClipboard("shared on purpose"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-got:
		if text != "shared on purpose" {
			t.Fatalf("viewer got %q, want the text SendClipboard was given", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendClipboard's text never reached the viewer")
	}
	select {
	case text := <-got:
		t.Fatalf("the viewer was also sent %q", text)
	case <-time.After(1 * time.Second):
	}
}

func TestSendClipboardNeedsConsent(t *testing.T) {
	got := make(chan string, 1)
	r, _, _ := dataSessionWith(t, noWatch(got)) // nothing granted
	if err := r.SendClipboard("x"); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("SendClipboard without consent: want ErrNotGranted, got %v", err)
	}
	select {
	case text := <-got:
		t.Fatalf("the viewer was sent %q without clipboard consent", text)
	case <-time.After(500 * time.Millisecond):
	}
}

// With nobody to send to over a data channel, SendClipboard says so rather
// than falling back to the signalling socket.
func TestSendClipboardNeedsADataChannel(t *testing.T) {
	r := New(Config{ID: "agent-1"})
	r.Grant(PermissionClipboard)
	if err := r.SendClipboard("x"); !errors.Is(err, ErrNoDataChannel) {
		t.Fatalf("SendClipboard with no session: want ErrNoDataChannel, got %v", err)
	}
}
