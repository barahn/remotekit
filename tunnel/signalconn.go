// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"errors"

	"github.com/gorilla/websocket"
)

// SignalConn is the transport a signalling session runs over: one message in,
// one message out. Start dials the control plane's WebSocket and uses that;
// Serve runs a session over any other -- a relay network, a local rendezvous,
// an in-memory pipe in a test -- without the session logic changing.
//
// Messages are whole JSON documents, the same ones the WebSocket carries.
// Implementations must keep message boundaries: one WriteMessage on one side is
// one ReadMessage on the other.
//
// Concurrency: ReadMessage is called from one goroutine at a time, and so is
// WriteMessage, but the two run concurrently with each other. Close may be
// called at any time, including while ReadMessage is blocked, and must make it
// return an error; it may be called more than once.
//
// The transport carries; it does not vouch. Whatever authenticates a viewer --
// RequireSignedViewers, consent -- applies the same over any SignalConn, and a
// transport that cannot be trusted any more than the control plane needs
// exactly those checks turned on.
type SignalConn interface {
	// ReadMessage blocks until the next message arrives. Any error ends the
	// session.
	ReadMessage() ([]byte, error)
	// WriteMessage sends one message.
	WriteMessage(data []byte) error
	// Close ends the connection and unblocks ReadMessage.
	Close() error
}

// wsSignalConn adapts the control plane's WebSocket to SignalConn.
type wsSignalConn struct{ ws *websocket.Conn }

func (c wsSignalConn) ReadMessage() ([]byte, error) {
	_, data, err := c.ws.ReadMessage()
	return data, err
}

func (c wsSignalConn) WriteMessage(data []byte) error {
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c wsSignalConn) Close() error { return c.ws.Close() }

// errNoAgentID is why Serve refuses a runner without credentials.
var errNoAgentID = errors.New("agent_stream: runner has no agent ID")

// Serve runs one signalling session over conn: the same session Start runs
// over the control plane's WebSocket -- negotiation, the data plane, consent,
// every check the runner is configured with -- but over a transport the
// caller supplies. It returns when conn fails or ctx is cancelled, and closes
// conn either way.
//
// Unlike Start it does not reconnect: a transport that can fail and come back
// is the caller's to redial, calling Serve again with the new connection.
//
// The runner still needs credentials, for the agent ID that viewers target and
// for the device key its answers are signed with; no token is needed, since
// Serve dials nothing. It returns ctx.Err() if ctx was cancelled and nil if the
// connection ended on its own.
func (r *AgentStreamRunner) Serve(ctx context.Context, conn SignalConn) error {
	if r.creds == nil || r.creds.AgentID == "" {
		return errNoAgentID
	}
	signKey, err := signalSigningKey(r.creds)
	if err != nil {
		return err
	}

	// ReadMessage blocks; closing the connection is what unblocks it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	r.runSignalingLoop(ctx, conn, signKey)
	_ = conn.Close()
	return ctx.Err()
}
