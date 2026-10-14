// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"errors"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/webrtc"
)

// The host's clipboard reaches viewers in one of two ways.
//
// By default the runner watches it, once PermissionClipboard is granted, and
// sends every change to every viewer: continuous sync, as a technician's tool
// expects. A consumer that promises the person at the machine never to read
// their clipboard on its own -- every copied password would otherwise follow
// -- turns the watch off with DisableClipboardWatch, and sends the clipboard
// with SendClipboard when that person asks.
//
// Text a viewer sends is applied to the host's clipboard either way, as far
// as PermissionClipboard allows: that writes the clipboard, it does not read
// it.

// ErrNotGranted is returned by SendClipboard when PermissionClipboard has not
// been granted.
var ErrNotGranted = errors.New("stream: the permission has not been granted")

// ErrNoDataChannel is returned by SendClipboard when no viewer has its
// control data channel open. The clipboard it sends never goes over the
// signalling socket, where the server in the middle could read it.
var ErrNoDataChannel = errors.New("stream: no viewer has a data channel open")

// DisableClipboardWatch stops the runner from watching the host's clipboard,
// so nothing the person at the machine copies is sent unless the consumer
// calls SendClipboard. Text from viewers is still applied as granted.
//
// It is opt-in, because turning it on changes what viewers get: the host's
// clipboard no longer follows them on its own.
//
// Call it before Serve.
func (r *Runner) DisableClipboardWatch(disabled bool) {
	r.noClipboardWatch = disabled
}

// SendClipboard sends text to every viewer, once, as the host's clipboard: for
// a consumer that shares the clipboard only when the person at the machine
// asks, with DisableClipboardWatch on. The text is the caller's to read; the
// runner does not read the clipboard for it.
//
// It needs PermissionClipboard (ErrNotGranted), and goes over the control
// data channel only: it fails with ErrNoDataChannel when no viewer has one
// open, and never falls back to the signalling socket. It returns nil when at
// least one viewer was sent the text.
func (r *Runner) SendClipboard(text string) error {
	if !r.allow(PermissionClipboard, "clipboard send") {
		return ErrNotGranted
	}
	r.sessionsMu.Lock()
	hooks := make([]*sessionHook, 0, len(r.sessions))
	for h := range r.sessions {
		hooks = append(hooks, h)
	}
	r.sessionsMu.Unlock()
	sent := 0
	for _, h := range hooks {
		if h.sendClipboard != nil {
			sent += h.sendClipboard(text)
		}
	}
	if sent == 0 {
		return ErrNoDataChannel
	}
	return nil
}

// clipboardSender returns what a running Serve uses to send the host's
// clipboard to the viewers in peers with an open control channel; it reports
// how many were sent it. lockPeers must hold the Serve's state lock for
// reading until the function it returns is called.
func clipboardSender(id string, lockPeers func() (map[string]*webrtc.PeerSession, func())) func(string) int {
	return func(text string) int {
		env, err := bark.EncodeEnvelope(bark.TypeClipboard, id, map[string]string{"text": text})
		if err != nil {
			return 0
		}
		peers, unlock := lockPeers()
		open := make([]*webrtc.PeerSession, 0, len(peers))
		for _, p := range peers {
			if p.DataChannelOpen(webrtc.DataChannelControl) {
				open = append(open, p)
			}
		}
		unlock()
		sent := 0
		for _, p := range open {
			if p.SendData(webrtc.DataChannelControl, env) == nil {
				sent++
			}
		}
		return sent
	}
}
