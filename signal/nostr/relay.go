// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gorilla/websocket"
)

// relayReadLimit bounds one frame from a relay. The largest event this package
// accepts carries a maximal NIP-44 payload (87472 base64 characters) plus the
// event's other fields; anything much bigger is not for us.
const relayReadLimit = 1 << 18

var errRelayClosedSub = errors.New("nostr: relay closed the subscription")

// relay is one NIP-01 connection: it publishes events and runs one
// subscription.
type relay struct {
	url     string
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func dialRelay(ctx context.Context, dialer *websocket.Dialer, url string) (*relay, error) {
	ws, resp, err := dialer.DialContext(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(relayReadLimit)
	return &relay{url: url, ws: ws}, nil
}

// send writes one NIP-01 message, a JSON array.
func (r *relay) send(msg ...any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.ws.WriteMessage(websocket.TextMessage, data)
}

func (r *relay) publish(ev *Event) error { return r.send("EVENT", ev) }

// filter is the NIP-01 subscription filter this package uses: gift wraps
// addressed to one key.
type filter struct {
	Kinds []int    `json:"kinds"`
	P     []string `json:"#p"`
	Since int64    `json:"since"`
}

func (r *relay) subscribe(subID string, f filter) error { return r.send("REQ", subID, f) }

// read delivers every event the relay sends on subID to onEvent, until the
// connection fails or the relay closes the subscription. OK, EOSE, NOTICE
// and AUTH are not needed for one-shot signalling and are ignored; an event
// the relay rejects is a message lost, which the session's own timeouts
// already cover.
func (r *relay) read(subID string, onEvent func(*Event)) error {
	for {
		_, data, err := r.ws.ReadMessage()
		if err != nil {
			return err
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
			continue
		}
		var label, sub string
		if json.Unmarshal(msg[0], &label) != nil || json.Unmarshal(msg[1], &sub) != nil {
			continue
		}
		switch label {
		case "EVENT":
			if sub != subID || len(msg) < 3 {
				continue
			}
			var ev Event
			if json.Unmarshal(msg[2], &ev) != nil {
				continue
			}
			onEvent(&ev)
		case "CLOSED":
			if sub == subID {
				return errRelayClosedSub
			}
		}
	}
}

func (r *relay) close() error { return r.ws.Close() }
