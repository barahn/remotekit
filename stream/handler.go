// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import "context"

// MessageHandler handles one signalling message type that the core does not
// implement itself.
//
// signal is the decoded message. id is the runner's session ID (see Config).
// reply writes a message back over the same connection and is safe for
// concurrent use, so a handler may keep calling it from a goroutine after it
// returns — which is how streaming responses are delivered.
//
// A handler is expected not to block the dispatch loop: do the work in a
// goroutine and stream results through reply.
type MessageHandler func(ctx context.Context, id string, signal map[string]interface{}, reply func([]byte) error)

// Handle registers a handler for a signalling message type, replacing any
// handler already registered for it. It returns the runner so registrations
// can be chained.
//
// This is the extension point that keeps product-specific features out of the
// core. The core implements the mechanism every remote session needs — screen,
// input, clipboard, file transfer, WebRTC negotiation — and anything layered on
// top, such as fleet script execution or power control, registers here instead
// of being compiled into the dispatch loop.
func (r *Runner) Handle(msgType string, h MessageHandler) *Runner {
	if r.handlers == nil {
		r.handlers = make(map[string]MessageHandler)
	}
	r.handlers[msgType] = h
	return r
}
