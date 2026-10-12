// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/screen"
	"github.com/barahn/remotekit/webrtc"
)

// Monitor selection (barahn/remotekit#94). A viewer asks for the agent's
// monitors and picks the one to stream, over the control data channel. The
// agent announces the list again whenever the monitor being streamed changes,
// so a viewer learns the outcome of its choice from the frames themselves.

// errNoCapture is why a monitor cannot be chosen before capture has started.
var errNoCapture = errors.New("screen capture has not started")

// minDisplaySwitch is the least time between two monitor switches in one
// session. Each forces a key frame on every viewer, so a viewer switching in a
// loop would otherwise keep the encoder producing nothing but key frames.
const minDisplaySwitch = 300 * time.Millisecond

// errDisplaySwitchTooSoon refuses a switch inside minDisplaySwitch.
var errDisplaySwitchTooSoon = errors.New("monitor switched too recently; try again")

// displaysPayload lists c's monitors, with active the one being streamed.
func displaysPayload(c screen.Capturer, active int) bark.DisplaysPayload {
	p := bark.DisplaysPayload{Active: active}
	list, err := c.Displays()
	if err != nil {
		p.Error = err.Error()
		return p
	}
	for _, d := range list {
		p.Displays = append(p.Displays, bark.DisplayInfo{
			Index:   d.Index,
			Name:    d.Name,
			X:       d.Bounds.Min.X,
			Y:       d.Bounds.Min.Y,
			Width:   d.Bounds.Dx(),
			Height:  d.Bounds.Dy(),
			Primary: d.Primary,
		})
	}
	return p
}

// isDisplayMessage reports whether a control channel message concerns monitor
// selection.
func isDisplayMessage(t bark.MessageType) bool {
	return t == bark.TypeListDisplays || t == bark.TypeSelectDisplay
}

// handleDisplayMessage answers list_displays and applies select_display, both
// with the current list. They are refused, unanswered, while the screen itself
// is withheld for want of consent: the list names the machine's monitors.
func (d *dataPlane) handleDisplayMessage(_ context.Context, peer *webrtc.PeerSession, label string, env bark.Envelope) {
	r := d.r
	if r.requireScreenView && !r.allow(PermissionScreenView, "monitor selection") {
		return
	}
	if d.displays == nil {
		return
	}
	var selectErr error
	if env.Type == bark.TypeSelectDisplay {
		var p bark.SelectDisplayPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		selectErr = d.selectDisplay(p.Index)
	}
	payload, ok := d.displays()
	if !ok {
		if selectErr == nil {
			return // nothing captured yet; the list follows the first frame
		}
		payload = bark.DisplaysPayload{Active: -1}
	}
	if selectErr != nil {
		payload.Error = selectErr.Error()
	}
	data, err := bark.EncodeEnvelope(bark.TypeDisplays, r.id, payload)
	if err != nil {
		return
	}
	_ = peer.SendData(label, data)
}
