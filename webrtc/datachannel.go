// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package webrtc

import (
	"errors"
	"fmt"

	"github.com/pion/webrtc/v4"
)

// Labels of the data channels the bark data plane runs over. Both are ordered
// and reliable: a keystroke delivered out of order types the wrong thing, and a
// lost file chunk corrupts the file.
const (
	// DataChannelControl carries input, clipboard and other control messages.
	DataChannelControl = "bark-control"
	// DataChannelTransfer carries file transfer, so a large file does not
	// queue keystrokes behind its chunks.
	DataChannelTransfer = "bark-transfer"
)

// ErrDataChannelNotOpen is returned by SendData when no open channel has the
// requested label.
var ErrDataChannelNotOpen = errors.New("webrtc: data channel not open")

// OpenDataChannel creates an ordered, reliable data channel with the given
// label. It is for the side that makes the offer, and must be called before
// CreateOffer so the offer carries the channel.
//
// The agent never calls it: the viewer offers, so the viewer creates the
// channels and the agent accepts them as they arrive (see OnDataMessage). It
// exists for viewers written in Go and for tests.
func (ps *PeerSession) OpenDataChannel(label string) error {
	ps.mu.RLock()
	closed := ps.closed
	ps.mu.RUnlock()
	if closed {
		return errors.New("webrtc: peer session closed")
	}

	dc, err := ps.pc.CreateDataChannel(label, nil) // nil: ordered and reliable
	if err != nil {
		return fmt.Errorf("webrtc: failed to create data channel %q: %w", label, err)
	}
	ps.attachDataChannel(dc)
	return nil
}

// OnDataMessage registers fn for every message arriving on a data channel,
// whichever side created it, with the label of the channel it came on.
//
// Register it before the negotiation that brings the channel up: a message
// arriving while no callback is set is dropped. The callback runs on pion's
// goroutine, one message at a time per channel, and should not block for long.
func (ps *PeerSession) OnDataMessage(fn func(label string, msg []byte)) {
	ps.dcMu.Lock()
	defer ps.dcMu.Unlock()
	ps.onDataMessage = fn
}

// OnDataChannelOpen registers fn to be told each time a data channel becomes
// usable, with its label. From then on SendData with that label succeeds.
func (ps *PeerSession) OnDataChannelOpen(fn func(label string)) {
	ps.dcMu.Lock()
	defer ps.dcMu.Unlock()
	ps.onDataChannelOpen = fn
}

// DataChannelOpen reports whether an open channel has the given label.
func (ps *PeerSession) DataChannelOpen(label string) bool {
	ps.dcMu.RLock()
	defer ps.dcMu.RUnlock()
	_, ok := ps.dataChannels[label]
	return ok
}

// SendData sends one message on the open channel with the given label.
//
// It goes out as a text message, since bark envelopes are JSON and a browser
// then hands them to its handler as a string rather than an ArrayBuffer.
// ErrDataChannelNotOpen means there is no such channel yet, or it has closed;
// the caller decides whether to wait, fall back or drop the message.
func (ps *PeerSession) SendData(label string, data []byte) error {
	ps.dcMu.RLock()
	dc, ok := ps.dataChannels[label]
	ps.dcMu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrDataChannelNotOpen, label)
	}
	return dc.SendText(string(data))
}

// acceptDataChannel handles a channel the remote peer created.
//
// Only ordered, reliable channels are kept. The data plane depends on both,
// and a peer that asks for less -- unordered, or with a retransmit or lifetime
// limit -- is closed rather than allowed to lose or reorder input silently.
func (ps *PeerSession) acceptDataChannel(dc *webrtc.DataChannel) {
	if !dc.Ordered() || dc.MaxRetransmits() != nil || dc.MaxPacketLifeTime() != nil {
		_ = dc.Close()
		return
	}
	ps.attachDataChannel(dc)
}

// attachDataChannel wires a channel into the session: messages go to
// OnDataMessage, and once open it is reachable by label through SendData.
// A second channel with a label already in use replaces the first.
func (ps *PeerSession) attachDataChannel(dc *webrtc.DataChannel) {
	label := dc.Label()

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		ps.dcMu.RLock()
		fn := ps.onDataMessage
		ps.dcMu.RUnlock()
		if fn != nil {
			fn(label, msg.Data)
		}
	})

	opened := func() {
		ps.dcMu.Lock()
		if ps.dataChannels[label] == dc {
			ps.dcMu.Unlock()
			return // already announced
		}
		if ps.dataChannels == nil {
			ps.dataChannels = make(map[string]*webrtc.DataChannel)
		}
		ps.dataChannels[label] = dc
		fn := ps.onDataChannelOpen
		ps.dcMu.Unlock()
		if fn != nil {
			fn(label)
		}
	}
	dc.OnOpen(opened)
	// A channel the remote peer created may already be open by the time it is
	// handed over, in which case OnOpen has nothing left to report.
	if dc.ReadyState() == webrtc.DataChannelStateOpen {
		opened()
	}

	dc.OnClose(func() {
		ps.dcMu.Lock()
		if ps.dataChannels[label] == dc {
			delete(ps.dataChannels, label)
		}
		ps.dcMu.Unlock()
	})
}
