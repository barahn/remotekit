// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/jezek/xgb/xproto"
)

// errNoAnswer is a selection owner that did not answer a conversion in time.
var errNoAnswer = errors.New("clipboard: the selection owner did not answer")

// Concealed reports whether the clipboard holds a secret, by the
// x-kde-passwordManagerHint target its owner offers: through X11 when Chirp's
// own connection works, else through wl-paste on Wayland. Text this process
// put there itself is never concealed.
func (lc *LinuxClipboard) Concealed(ctx context.Context) (bool, error) {
	if d := lc.x11Driver; d != nil {
		if concealed, ok := d.concealed(); ok {
			return concealed, nil
		}
	}
	return waylandConcealed(ctx)
}

// concealed asks the X11 clipboard owner for its targets; ok is false when
// it could not tell.
func (d *x11Driver) concealed() (concealed, ok bool) {
	if d.conn == nil || d.readerConn == nil {
		return false, false
	}
	owner, err := xproto.GetSelectionOwner(d.conn, d.clipAtom).Reply()
	if err != nil {
		return false, false
	}
	if owner.Owner == d.win || owner.Owner == 0 {
		return false, true
	}

	d.readerMu.Lock()
	defer d.readerMu.Unlock()
	raw, err := d.convert("TARGETS")
	if err != nil {
		return false, false
	}
	var targets []string
	for i := 0; i+4 <= len(raw); i += 4 {
		atom := xproto.Atom(binary.LittleEndian.Uint32(raw[i:]))
		if name, err := xproto.GetAtomName(d.readerConn, atom).Reply(); err == nil {
			targets = append(targets, name.Name)
		}
	}
	return concealedByTargets(targets, d.convert), true
}

// convert asks the clipboard owner for its content as target, on the reader
// connection. The caller holds readerMu.
func (d *x11Driver) convert(target string) ([]byte, error) {
	prop := internAtom(d.readerConn, "BARAHN_TEMP_READ_PROP")
	clip := internAtom(d.readerConn, "CLIPBOARD")
	want := internAtom(d.readerConn, target)
	xproto.ConvertSelection(d.readerConn, d.readerWin, clip, want, prop, xproto.TimeCurrentTime)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		ev, xerr := d.readerConn.PollForEvent()
		if xerr != nil {
			return nil, xerr
		}
		if ev == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		n, ok := ev.(xproto.SelectionNotifyEvent)
		if !ok {
			continue
		}
		if n.Property == 0 {
			return nil, errNoAnswer
		}
		reply, err := xproto.GetProperty(d.readerConn, true, d.readerWin, n.Property, xproto.GetPropertyTypeAny, 0, 1024*1024).Reply()
		if err != nil {
			return nil, err
		}
		return reply.Value, nil
	}
	return nil, errNoAnswer
}

// waylandConcealed asks wl-paste for the offered types. Without wl-paste
// there is no way to tell, and nothing marked can be read either: not
// concealed.
func waylandConcealed(ctx context.Context) (bool, error) {
	path, err := exec.LookPath("wl-paste")
	if err != nil {
		return false, nil
	}
	out, err := exec.CommandContext(ctx, path, "--list-types").Output() // #nosec G204 -- path resolved via exec.LookPath
	if err != nil {
		return false, nil // an empty clipboard, or no Wayland session
	}
	types := strings.Fields(string(out))
	return concealedByTargets(types, func(target string) ([]byte, error) {
		v, err := exec.CommandContext(ctx, path, "--no-newline", "--type", target).Output() // #nosec G204 -- path resolved via exec.LookPath; target is a constant
		return bytes.TrimSpace(v), err
	}), nil
}
