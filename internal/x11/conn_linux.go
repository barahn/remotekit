// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build linux

// Package x11 opens X11 connections and enables extensions on them without
// racing inside github.com/jezek/xgb.
//
// xgb's extension Init functions (randr.Init, shm.Init, xtest.Init, ...) do two
// things: record the extension's major opcode on the connection, guarded by
// Conn.ExtLock, and register the extension's event and error constructors in
// the package-level xgb.NewEventFuncs and xgb.NewErrorFuncs maps, without any
// lock. Every connection's readResponses goroutine reads those maps whenever
// an event or error arrives. So calling an Init while any xgb connection is
// alive - the clipboard's event loop, say - is a data race, and can crash the
// process with "concurrent map read and map write". This is so on xgb v1.3.1,
// the latest release, and on its master branch.
//
// The constructors registered for a display are the same for every
// connection to it, so they only need registering once. NewConn does that on
// the first connection it returns, before any caller reads them; Enable then
// records the opcode on each later connection, touching only the
// per-connection state. Every xgb connection in remotekit must be opened
// through NewConn, and extensions enabled through Enable, for this to hold.
package x11

import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Extension names as the X server knows them.
const (
	RANDR = "RANDR"
	SHM   = "MIT-SHM"
	XTest = "XTEST"
)

// extensionInits are the xgb Init functions for every extension remotekit
// uses. Each registers its constructors in xgb's global maps.
var extensionInits = map[string]func(*xgb.Conn) error{
	RANDR: randr.Init,
	SHM:   shm.Init,
	XTest: xtest.Init,
}

var (
	registerMu sync.Mutex
	registered bool
)

// NewConn connects to the X server named by $DISPLAY, as xgb.NewConn does.
// The first connection registers the global extension event and error
// constructors before handing out the connection; later calls skip this.
func NewConn() (*xgb.Conn, error) {
	registerMu.Lock()
	defer registerMu.Unlock()

	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	if !registered {
		for _, initExt := range extensionInits {
			_ = initExt(conn)
		}
		registered = true
	}
	return conn, nil
}

// Enable makes the named extension usable on conn, which must come from
// NewConn. It is the per-connection half of the extension's Init function.
func Enable(conn *xgb.Conn, name string) error {
	if _, ok := extensionInits[name]; !ok {
		return fmt.Errorf("x11: extension %s is not registered", name)
	}
	reply, err := xproto.QueryExtension(conn, uint16(len(name)), name).Reply() // #nosec G115 -- extension names are short constants
	if err != nil {
		return err
	}
	if !reply.Present {
		return fmt.Errorf("x11: extension %s is not present on the server", name)
	}
	conn.ExtLock.Lock()
	conn.Extensions[name] = reply.MajorOpcode
	conn.ExtLock.Unlock()
	return nil
}
