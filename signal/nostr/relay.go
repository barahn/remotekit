// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// relayReadLimit bounds one frame from a relay. The largest event this package
// accepts carries a maximal NIP-44 payload (87472 base64 characters) plus the
// event's other fields; anything much bigger is not for us.
const relayReadLimit = 1 << 18

var errRelayClosedSub = errors.New("nostr: relay closed the subscription")

// relay is one NIP-01 connection: it publishes events and runs one
// subscription, answering NIP-42 authentication challenges when required.
type relay struct {
	url        string
	ws         *websocket.Conn
	writeMu    sync.Mutex
	authKey    *SecretKey
	lastAuthID string
	subFilter  *filter
}

func dialRelay(ctx context.Context, dialer *websocket.Dialer, url string, authKey *SecretKey) (*relay, error) {
	ws, resp, err := dialer.DialContext(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(relayReadLimit)
	return &relay{url: url, ws: ws, authKey: authKey}, nil
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

func (r *relay) subscribe(subID string, f filter) error {
	r.writeMu.Lock()
	r.subFilter = &f
	r.writeMu.Unlock()
	return r.send("REQ", subID, f)
}

// read delivers every event the relay sends on subID to onEvent, until the
// connection fails or the relay closes the subscription. It answers NIP-42
// AUTH challenges with a kind 22242 event, and returns ErrAuthRefused if
// the relay rejects authentication.
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
		var label, param string
		if json.Unmarshal(msg[0], &label) != nil || json.Unmarshal(msg[1], &param) != nil {
			continue
		}
		switch label {
		case "EVENT":
			if param != subID || len(msg) < 3 {
				continue
			}
			var ev Event
			if json.Unmarshal(msg[2], &ev) != nil {
				continue
			}
			onEvent(&ev)
		case "CLOSED":
			var reason string
			if len(msg) >= 3 {
				_ = json.Unmarshal(msg[2], &reason)
			}
			if strings.HasPrefix(reason, "auth-required:") {
				continue
			}
			if param == subID {
				return errRelayClosedSub
			}
		case "AUTH":
			challenge := param
			if r.authKey == nil {
				continue
			}
			authEv := &Event{
				CreatedAt: time.Now().Unix(),
				Kind:      KindAuth,
				Tags: [][]string{
					{"relay", r.url},
					{"challenge", challenge},
				},
				Content: "",
			}
			if err := authEv.sign(r.authKey); err != nil {
				return err
			}
			if err := r.send("AUTH", authEv); err != nil {
				return err
			}
			r.writeMu.Lock()
			r.lastAuthID = authEv.ID
			r.writeMu.Unlock()
		case "OK":
			var accepted bool
			var reason string
			if len(msg) >= 3 {
				_ = json.Unmarshal(msg[2], &accepted)
			}
			if len(msg) >= 4 {
				_ = json.Unmarshal(msg[3], &reason)
			}
			r.writeMu.Lock()
			isAuth := param == r.lastAuthID && r.lastAuthID != ""
			filterToResend := r.subFilter
			r.writeMu.Unlock()

			if isAuth {
				if !accepted {
					return fmt.Errorf("%w: %s", ErrAuthRefused, reason)
				}
				if filterToResend != nil {
					_ = r.send("REQ", subID, *filterToResend)
				}
			}
		}
	}
}

func (r *relay) close() error { return r.ws.Close() }
