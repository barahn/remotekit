//go:build !windows

package clipboard

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// LinuxClipboard implements system clipboard reading and writing on Linux (Native X11, Wayland, CLI tools).
type LinuxClipboard struct {
	mu             sync.RWMutex
	memoryFallback string
	x11Driver      *x11Driver
}

type x11Driver struct {
	mu            sync.RWMutex
	conn          *xgb.Conn
	win           xproto.Window
	clipAtom      xproto.Atom
	primaryAtom   xproto.Atom
	utf8Atom      xproto.Atom
	stringAtom    xproto.Atom
	textAtom      xproto.Atom
	plainAtom     xproto.Atom
	plainUtf8Atom xproto.Atom
	targetsAtom   xproto.Atom
	propAtom      xproto.Atom
	text          string
	stopChan      chan struct{}
}

func internAtom(conn *xgb.Conn, name string) xproto.Atom {
	reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	return reply.Atom
}

func initX11Driver() *x11Driver {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil
	}

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	win, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil
	}

	err = xproto.CreateWindowChecked(conn, screen.RootDepth, win, screen.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, screen.RootVisual, 0, []uint32{}).Check()
	if err != nil {
		conn.Close()
		return nil
	}

	d := &x11Driver{
		conn:          conn,
		win:           win,
		clipAtom:      internAtom(conn, "CLIPBOARD"),
		primaryAtom:   internAtom(conn, "PRIMARY"),
		utf8Atom:      internAtom(conn, "UTF8_STRING"),
		stringAtom:    internAtom(conn, "STRING"),
		textAtom:      internAtom(conn, "TEXT"),
		plainAtom:     internAtom(conn, "text/plain"),
		plainUtf8Atom: internAtom(conn, "text/plain;charset=utf-8"),
		targetsAtom:   internAtom(conn, "TARGETS"),
		propAtom:      internAtom(conn, "BARAHN_CLIPBOARD_PROP"),
		stopChan:      make(chan struct{}),
	}

	go d.eventLoop()
	return d
}

func (d *x11Driver) eventLoop() {
	for {
		select {
		case <-d.stopChan:
			return
		default:
			ev, err := d.conn.WaitForEvent()
			if err != nil {
				return
			}
			if req, ok := ev.(xproto.SelectionRequestEvent); ok {
				d.handleSelectionRequest(req)
			}
		}
	}
}

func (d *x11Driver) handleSelectionRequest(req xproto.SelectionRequestEvent) {
	d.mu.RLock()
	currentText := d.text
	d.mu.RUnlock()

	respProperty := req.Property
	if respProperty == 0 {
		respProperty = req.Target
	}

	if req.Target == d.targetsAtom {
		targets := []uint32{
			uint32(d.targetsAtom),
			uint32(d.utf8Atom),
			uint32(d.plainUtf8Atom),
			uint32(d.plainAtom),
			uint32(d.stringAtom),
			uint32(d.textAtom),
		}
		raw := make([]byte, len(targets)*4)
		for i, t := range targets {
			raw[i*4] = byte(t)
			raw[i*4+1] = byte(t >> 8)
			raw[i*4+2] = byte(t >> 16)
			raw[i*4+3] = byte(t >> 24)
		}
		_ = xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, req.Requestor, respProperty, xproto.AtomAtom, 32, uint32(len(targets)), raw).Check()
	} else if req.Target == d.utf8Atom || req.Target == d.stringAtom || req.Target == d.textAtom || req.Target == d.plainUtf8Atom || req.Target == d.plainAtom {
		data := []byte(currentText)
		_ = xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, req.Requestor, respProperty, req.Target, 8, uint32(len(data)), data).Check()
	} else {
		respProperty = 0
	}

	notify := xproto.SelectionNotifyEvent{
		Time:      req.Time,
		Requestor: req.Requestor,
		Selection: req.Selection,
		Target:    req.Target,
		Property:  respProperty,
	}

	_ = xproto.SendEventChecked(d.conn, false, req.Requestor, 0, string(notify.Bytes())).Check()
}

func (d *x11Driver) SetText(text string) error {
	d.mu.Lock()
	d.text = text
	d.mu.Unlock()

	now := xproto.Timestamp(xproto.TimeCurrentTime)
	_ = xproto.SetSelectionOwnerChecked(d.conn, d.win, d.clipAtom, now).Check()
	_ = xproto.SetSelectionOwnerChecked(d.conn, d.win, d.primaryAtom, now).Check()
	return nil
}

func (d *x11Driver) GetText() (string, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return d.text, nil
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	tmpWin, err := xproto.NewWindowId(conn)
	if err != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return d.text, nil
	}

	err = xproto.CreateWindowChecked(conn, screen.RootDepth, tmpWin, screen.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, screen.RootVisual, 0, []uint32{}).Check()
	if err != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return d.text, nil
	}
	defer xproto.DestroyWindow(conn, tmpWin)

	clipAtom := internAtom(conn, "CLIPBOARD")
	utf8Atom := internAtom(conn, "UTF8_STRING")
	propAtom := internAtom(conn, "BARAHN_TEMP_READ_PROP")

	xproto.ConvertSelection(conn, tmpWin, clipAtom, utf8Atom, propAtom, xproto.TimeCurrentTime)

	timeout := time.After(150 * time.Millisecond)
	for {
		select {
		case <-timeout:
			d.mu.RLock()
			defer d.mu.RUnlock()
			return d.text, nil
		default:
			ev, err := conn.PollForEvent()
			if err != nil {
				d.mu.RLock()
				defer d.mu.RUnlock()
				return d.text, nil
			}
			if ev == nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			if selNotify, ok := ev.(xproto.SelectionNotifyEvent); ok {
				if selNotify.Property == 0 {
					d.mu.RLock()
					defer d.mu.RUnlock()
					return d.text, nil
				}
				prop, err := xproto.GetProperty(conn, true, tmpWin, selNotify.Property, xproto.GetPropertyTypeAny, 0, 1024*1024).Reply()
				if err != nil {
					d.mu.RLock()
					defer d.mu.RUnlock()
					return d.text, nil
				}
				return string(prop.Value), nil
			}
		}
	}
}

// NewManager returns the cross-platform Linux clipboard manager.
func NewManager() Manager {
	driver := initX11Driver()
	return &LinuxClipboard{
		x11Driver: driver,
	}
}

func (lc *LinuxClipboard) GetText(ctx context.Context) (string, error) {
	// 1. Try Native Pure Go X11 Driver
	if lc.x11Driver != nil {
		if text, err := lc.x11Driver.GetText(); err == nil && text != "" {
			return text, nil
		}
	}

	// 2. Try wl-paste (Wayland)
	if path, err := exec.LookPath("wl-paste"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--no-newline") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil && out.Len() > 0 {
			return out.String(), nil
		}
	}

	// 3. Try xclip (X11 CLI fallback)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, path, "-selection", "clipboard", "-o") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil && out.Len() > 0 {
			return out.String(), nil
		}
	}

	// 4. Try xsel (X11 CLI fallback)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--clipboard", "--output") // #nosec G204 -- path resolved via exec.LookPath
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil && out.Len() > 0 {
			return out.String(), nil
		}
	}

	// 5. Memory fallback
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.memoryFallback, nil
}

func (lc *LinuxClipboard) SetText(ctx context.Context, text string) error {
	lc.mu.Lock()
	lc.memoryFallback = text
	lc.mu.Unlock()

	// 1. Try Native Pure Go X11 Driver
	if lc.x11Driver != nil {
		if err := lc.x11Driver.SetText(text); err == nil {
			return nil
		}
	}

	// 2. Try wl-copy (Wayland)
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.CommandContext(ctx, path) // #nosec G204 -- path resolved via exec.LookPath
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	// 3. Try xclip (X11 CLI fallback)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, path, "-selection", "clipboard") // #nosec G204 -- path resolved via exec.LookPath
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	// 4. Try xsel (X11 CLI fallback)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.CommandContext(ctx, path, "--clipboard", "--input") // #nosec G204 -- path resolved via exec.LookPath
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return nil
}
