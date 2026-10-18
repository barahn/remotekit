// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package clipboard

import (
	"context"
	"encoding/binary"
	"os"
	"sync/atomic"
	"testing"

	"github.com/barahn/remotekit/internal/x11"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// A password manager on X11 -- played here by a window of the test's own --
// owns the clipboard and offers x-kde-passwordManagerHint; Concealed sees it.
//
// It takes the real clipboard of the display it runs on, so it runs only when
// asked: REMOTEKIT_X11_CLIPBOARD_TEST=1, preferably under Xvfb.
func TestX11ConcealedSeesThePasswordManagerHint(t *testing.T) {
	if os.Getenv("REMOTEKIT_X11_CLIPBOARD_TEST") != "1" {
		t.Skip("takes the display's clipboard; set REMOTEKIT_X11_CLIPBOARD_TEST=1 to run")
	}
	conn, err := x11.NewConn()
	if err != nil {
		t.Skip("no X11 display:", err)
	}
	defer conn.Close()
	screen := xproto.Setup(conn).DefaultScreen(conn)
	win, _ := xproto.NewWindowId(conn)
	if err := xproto.CreateWindowChecked(conn, screen.RootDepth, win, screen.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	atom := func(name string) xproto.Atom { return internAtom(conn, name) }
	clip, targets, utf8, hint := atom("CLIPBOARD"), atom("TARGETS"), atom("UTF8_STRING"), atom(kdePasswordHint)

	cb := NewManager().(*LinuxClipboard)
	if cb.x11Driver == nil {
		t.Skip("the clipboard's X11 driver did not start")
	}

	if err := xproto.SetSelectionOwnerChecked(conn, win, clip, xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	var secret atomic.Bool
	secret.Store(true)
	go serveSelection(conn, targets, utf8, hint, &secret)

	if got, err := cb.Concealed(context.Background()); err != nil || !got {
		t.Fatalf("with the hint offered: Concealed = %v, %v; want true", got, err)
	}
	secret.Store(false)
	if got, err := cb.Concealed(context.Background()); err != nil || got {
		t.Fatalf("without the hint: Concealed = %v, %v; want false", got, err)
	}
}

// serveSelection answers selection requests as an owner offering text, and
// the password manager hint when secret.
func serveSelection(conn *xgb.Conn, targets, utf8, hint xproto.Atom, secret *atomic.Bool) {
	for {
		ev, err := conn.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		req, ok := ev.(xproto.SelectionRequestEvent)
		if !ok {
			continue
		}
		prop := req.Property
		switch req.Target {
		case targets:
			list := []xproto.Atom{targets, utf8}
			if secret.Load() {
				list = append(list, hint)
			}
			raw := make([]byte, 4*len(list))
			for i, a := range list {
				binary.LittleEndian.PutUint32(raw[4*i:], uint32(a))
			}
			xproto.ChangeProperty(conn, xproto.PropModeReplace, req.Requestor, prop, xproto.AtomAtom, 32, uint32(len(list)), raw)
		case hint:
			xproto.ChangeProperty(conn, xproto.PropModeReplace, req.Requestor, prop, hint, 8, 6, []byte("secret"))
		case utf8:
			xproto.ChangeProperty(conn, xproto.PropModeReplace, req.Requestor, prop, utf8, 8, 7, []byte("hunter2"))
		default:
			prop = 0
		}
		n := xproto.SelectionNotifyEvent{Time: req.Time, Requestor: req.Requestor, Selection: req.Selection, Target: req.Target, Property: prop}
		xproto.SendEvent(conn, false, req.Requestor, 0, string(n.Bytes()))
	}
}
