// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"encoding/json"
	"log"
	"sync"
)

// Relay mode is the JPEG fallback: while a viewer cannot be served over
// WebRTC -- negotiation unfinished, failed, or a codec its browser cannot
// decode -- frames go as JPEG over the signalling socket, and the server in
// the middle can see the screen. It used to happen silently. Now it is always
// announced, and a consumer can refuse it.

// relayNotice is what the viewer is sent on entering relay mode, and on
// leaving it with active set to false.
const relayNotice = "relay_mode"

// relayRefusedNotice is what the viewer is sent, instead of frames, when relay
// mode is needed but DisableRelayFallback is on.
const relayRefusedNotice = "relay_fallback_refused"

// relayReason is the text a viewer can show as is.
const relayReason = "relay mode: the screen is relayed through the server, which can see it"

// DisableRelayFallback refuses the JPEG fallback, so the screen never passes
// through the server: a viewer that is not served over WebRTC gets
// {"type":"relay_fallback_refused"} instead of frames, and waits for its
// direct connection.
//
// It is opt-in, because turning it on changes what every viewer gets: one
// that cannot negotiate WebRTC, or cannot decode VP8, sees no screen at all.
// Without it the fallback works as before, but is never silent: entering it
// sends the viewer {"type":"relay_mode","active":true,"reason":...}, leaving
// it sends active false, and both are logged and reported to OnRelayMode.
//
// Call it before Serve.
func (r *Runner) DisableRelayFallback(disabled bool) {
	r.disableRelay = disabled
}

// OnRelayMode registers fn to be told when the session enters relay mode
// (true) and leaves it (false), so the agent's own UI -- a tray icon, say --
// can show the person at the machine that the server can see the screen. It
// runs on the frame sender's goroutine and must not block.
//
// Call it before Serve.
func (r *Runner) OnRelayMode(fn func(active bool)) {
	r.onRelayMode = fn
}

// relayGate tracks relay mode for one signalling connection and does the
// announcing, so the frame loop only asks whether it may send a JPEG frame.
type relayGate struct {
	disabled bool
	onChange func(active bool)

	mu     sync.Mutex
	active bool
	// told and toldRefused say whether the viewers on the socket have been
	// sent the current notice. They are cleared when a viewer joins, which
	// has not seen it.
	told        bool
	toldRefused bool
}

// relay is called for each frame that would go as JPEG. It reports whether it
// may; on the way into relay mode, it tells the viewers and the consumer.
func (g *relayGate) relay(write func([]byte) error) bool {
	g.mu.Lock()
	if g.disabled {
		tell := !g.toldRefused
		g.toldRefused = true
		g.mu.Unlock()
		if tell {
			log.Printf("[AgentStream] Relay mode refused: no viewer is served over WebRTC and the JPEG fallback is disabled\n")
			notice, _ := json.Marshal(map[string]string{"type": relayRefusedNotice})
			_ = write(notice)
		}
		return false
	}
	entering := !g.active
	tell := !g.told
	g.active, g.told = true, true
	g.mu.Unlock()

	if tell {
		notice, _ := json.Marshal(map[string]interface{}{
			"type":   relayNotice,
			"active": true,
			"reason": relayReason,
		})
		_ = write(notice)
	}
	if entering {
		log.Printf("[AgentStream] Entering relay mode: frames go through the server as JPEG, and the server can see the screen\n")
		if g.onChange != nil {
			g.onChange(true)
		}
	}
	return true
}

// direct is called when every viewer is served over WebRTC; it leaves relay
// mode if the session was in it.
func (g *relayGate) direct(write func([]byte) error) {
	g.mu.Lock()
	leaving := g.active
	g.active, g.told, g.toldRefused = false, false, false
	g.mu.Unlock()
	if !leaving {
		return
	}
	notice, _ := json.Marshal(map[string]interface{}{"type": relayNotice, "active": false})
	_ = write(notice)
	log.Printf("[AgentStream] Leaving relay mode: every viewer is served over WebRTC\n")
	if g.onChange != nil {
		g.onChange(false)
	}
}

// viewerJoined makes the next relayed frame announce relay mode again, since
// the new viewer did not see the earlier notice.
func (g *relayGate) viewerJoined() {
	g.mu.Lock()
	g.told, g.toldRefused = false, false
	g.mu.Unlock()
}

// stop ends relay mode without a notice, as the session itself is ending.
func (g *relayGate) stop() {
	g.mu.Lock()
	leaving := g.active
	g.active, g.told, g.toldRefused = false, false, false
	g.mu.Unlock()
	if leaving && g.onChange != nil {
		g.onChange(false)
	}
}
