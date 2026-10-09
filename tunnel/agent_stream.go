// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/barahn/remotekit/stream"
	"github.com/gorilla/websocket"
)

// The session runner lives in package stream; these names keep tunnel's
// callers compiling unchanged.
type (
	// SignalConn is stream.SignalConn.
	SignalConn = stream.SignalConn
	// MessageHandler is stream.MessageHandler.
	MessageHandler = stream.MessageHandler
)

// The permissions a session can be granted; see package stream.
const (
	PermissionScreenView    = stream.PermissionScreenView
	PermissionRemoteControl = stream.PermissionRemoteControl
	PermissionClipboard     = stream.PermissionClipboard
	PermissionFileTransfer  = stream.PermissionFileTransfer
	PermissionReverseStream = stream.PermissionReverseStream
)

// Errors for a viewer message refused by RequireSignedViewers; see package
// stream.
var (
	ErrViewerUntrusted   = stream.ErrViewerUntrusted
	ErrViewerWrongTarget = stream.ErrViewerWrongTarget
	ErrViewerReplay      = stream.ErrViewerReplay
)

// AgentStreamRunner is a stream.Runner for an enrolled agent: its session ID
// is the agent ID and its answers are signed with the device key, both taken
// from the agent's credentials, and Start serves it over the control plane's
// WebSocket. Every method of the embedded Runner -- Grant,
// RequireSignedViewers, Serve and the rest -- applies as documented there.
//
// The Runner is embedded by value, so a zero AgentStreamRunner can still
// Grant and report Granted, as it always could; it cannot Serve or Start.
type AgentStreamRunner struct {
	stream.Runner

	creds              *AgentCredentials
	insecureSkipVerify bool
	// keyErr is why the device key could not be loaded, if it could not;
	// Start and Serve refuse to run with it set rather than go out unsigned.
	keyErr error
}

// NewAgentStreamRunner builds a runner for the enrolled agent creds. The
// device key at creds.DeviceKeyPath, if any, is loaded here; failing to load
// it is reported by Start and Serve.
func NewAgentStreamRunner(creds *AgentCredentials, insecureSkipVerify bool) *AgentStreamRunner {
	key, err := signalSigningKey(creds)
	var id string
	if creds != nil {
		id = creds.AgentID
	}
	r := &AgentStreamRunner{
		creds:              creds,
		insecureSkipVerify: insecureSkipVerify,
		keyErr:             err,
	}
	r.Init(stream.Config{ID: id, SigningKey: key})
	return r
}

// Handle registers a handler for a signalling message type; see
// stream.Runner.Handle. It returns the runner so registrations can be
// chained.
func (r *AgentStreamRunner) Handle(msgType string, h MessageHandler) *AgentStreamRunner {
	r.Runner.Handle(msgType, h)
	return r
}

// errNoAgentID is why Serve refuses a runner without credentials.
var errNoAgentID = stream.ErrNoID

// Serve runs one signalling session over conn; see stream.Runner.Serve. The
// runner still needs credentials, for the agent ID that viewers target and
// for the device key its answers are signed with; no token is needed, since
// Serve dials nothing.
func (r *AgentStreamRunner) Serve(ctx context.Context, conn SignalConn) error {
	if r.creds == nil || r.creds.AgentID == "" {
		return errNoAgentID
	}
	if r.keyErr != nil {
		return r.keyErr
	}
	return r.Runner.Serve(ctx, conn)
}

// Start connects to the signalling channel and serves it until ctx is
// cancelled, reconnecting whenever the connection drops.
//
// It never connects without a credential: if the runner's AgentToken is
// empty, Start logs why and returns immediately, and nothing will retry.
// Start reports no error, so callers that need to know should check
// AgentToken themselves before calling it -- an empty token means the agent
// has to re-enrol.
func (r *AgentStreamRunner) Start(ctx context.Context) {
	headers, err := signalHeaders(r.creds)
	if err != nil {
		log.Printf("[AgentStream Error] %v; not connecting\n", err)
		return
	}
	if r.keyErr != nil {
		log.Printf("[AgentStream Error] %v; not connecting\n", r.keyErr)
		return
	}

	wsURL := r.creds.ServerAddr
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + wsURL[8:]
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + wsURL[7:]
	}
	signalURL := fmt.Sprintf("%s/api/v1/sessions/%s/signal", strings.TrimRight(wsURL, "/"), r.creds.AgentID)

	dialer := wsDialer(r.creds.ServerKeyPin, r.insecureSkipVerify)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// The proof carries a timestamp and a single-use nonce, so it
			// is rebuilt for every attempt rather than once: a reconnect
			// would otherwise present a stale or already used one.
			attempt := headers.Clone()
			if err := connectProofHeaders(attempt, r.creds, AudienceSignal, time.Now()); err != nil {
				log.Printf("[AgentStream Error] %v; not connecting\n", err)
				return
			}
			ws, _, err := dialer.DialContext(ctx, signalURL, attempt)
			if err != nil {
				log.Printf("[AgentStream Error] Failed to connect to signaling WebSocket (%s): %v\n", signalURL, err)
				time.Sleep(2 * time.Second)
				continue
			}

			ws.SetReadLimit(10 * 1024 * 1024) // 10MB limit for fallback JPEG frames

			log.Printf("[AgentStream] Connected to signaling channel for agent %s\n", r.creds.AgentID)
			if err := r.Serve(ctx, wsSignalConn{ws}); errors.Is(err, errNoAgentID) {
				log.Printf("[AgentStream Error] %v; not connecting\n", err)
				return
			}
			time.Sleep(1 * time.Second)
		}
	}
}

// errNoAgentToken is why Start refuses to dial without a credential.
var errNoAgentToken = errors.New("agent_stream: agent has no token; re-enrol before connecting")

// signalHeaders builds the headers the signalling socket authenticates with.
//
// It refuses to proceed without a token. It used to fall back to sending the
// agent ID in the token header, and the ID is not a secret -- the server hands
// it out and it appears in URLs and logs. A server that accepted that fallback
// would let anyone who had seen an ID connect as that agent.
func signalHeaders(creds *AgentCredentials) (http.Header, error) {
	if creds == nil || creds.AgentToken == "" {
		return nil, errNoAgentToken
	}
	h := http.Header{}
	h.Set("X-Barahn-Agent-Token", creds.AgentToken)
	return h, nil
}

// signalSigningKey loads the device key the agent signs its answers and ICE
// candidates with, or returns nil for an agent enrolled without one, whose
// messages go out unsigned as before.
func signalSigningKey(creds *AgentCredentials) (ed25519.PrivateKey, error) {
	if creds == nil || creds.DeviceKeyPath == "" {
		return nil, nil
	}
	priv, err := LoadDeviceKey(creds.DeviceKeyPath)
	if err != nil {
		return nil, fmt.Errorf("agent_stream: loading device key: %w", err)
	}
	return priv, nil
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
