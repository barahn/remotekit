// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// fakeRelay is just enough of a NIP-01 relay for these tests: it checks each
// event's signature as a real relay would, stores it, answers REQ with what
// it has stored and then streams new matches. It ignores expiration.
type fakeRelay struct {
	srv *httptest.Server

	mu     sync.Mutex
	events []*Event
	subs   map[*fakeClient]map[string]filter
	// published counts the EVENTs accepted, for tests that need to know
	// what reached the relay.
	published int
}

type fakeClient struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *fakeClient) send(msg ...any) {
	data, _ := json.Marshal(msg)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, data)
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	fr := &fakeRelay{subs: map[*fakeClient]map[string]filter{}}
	upgrader := websocket.Upgrader{}
	fr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &fakeClient{ws: ws}
		fr.mu.Lock()
		fr.subs[c] = map[string]filter{}
		fr.mu.Unlock()
		defer func() {
			fr.mu.Lock()
			delete(fr.subs, c)
			fr.mu.Unlock()
			_ = ws.Close()
		}()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			fr.handle(c, data)
		}
	}))
	t.Cleanup(fr.close)
	return fr
}

func (fr *fakeRelay) url() string { return "ws" + strings.TrimPrefix(fr.srv.URL, "http") }

// close drops every client connection and stops the server.
func (fr *fakeRelay) close() {
	fr.mu.Lock()
	for c := range fr.subs {
		_ = c.ws.Close()
	}
	fr.mu.Unlock()
	fr.srv.Close()
}

func (f filter) matches(ev *Event) bool {
	return slices.Contains(f.Kinds, ev.Kind) &&
		slices.Contains(f.P, ev.tag("p")) &&
		ev.CreatedAt >= f.Since
}

func (fr *fakeRelay) handle(c *fakeClient, data []byte) {
	var msg []json.RawMessage
	if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
		return
	}
	var label string
	_ = json.Unmarshal(msg[0], &label)
	switch label {
	case "EVENT":
		var ev Event
		if json.Unmarshal(msg[1], &ev) != nil {
			return
		}
		if err := ev.verify(); err != nil {
			c.send("OK", ev.ID, false, "invalid: "+err.Error())
			return
		}
		c.send("OK", ev.ID, true, "")
		fr.inject(&ev)
	case "REQ":
		var sub string
		var f filter
		if len(msg) < 3 || json.Unmarshal(msg[1], &sub) != nil || json.Unmarshal(msg[2], &f) != nil {
			return
		}
		fr.mu.Lock()
		fr.subs[c][sub] = f
		var stored []*Event
		for _, ev := range fr.events {
			if f.matches(ev) {
				stored = append(stored, ev)
			}
		}
		fr.mu.Unlock()
		for _, ev := range stored {
			c.send("EVENT", sub, ev)
		}
		c.send("EOSE", sub)
	}
}

// inject stores ev and sends it to every matching subscription, without
// checking it: tests use it to play a relay that misbehaves.
func (fr *fakeRelay) inject(ev *Event) {
	type delivery struct {
		c   *fakeClient
		sub string
	}
	fr.mu.Lock()
	fr.events = append(fr.events, ev)
	fr.published++
	var out []delivery
	for c, subs := range fr.subs {
		for sub, f := range subs {
			if f.matches(ev) {
				out = append(out, delivery{c, sub})
			}
		}
	}
	fr.mu.Unlock()
	for _, d := range out {
		d.c.send("EVENT", d.sub, ev)
	}
}

// closeSubscriptions sends CLOSED for every subscription, as a relay does
// when it stops serving one.
func (fr *fakeRelay) closeSubscriptions() {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for c, subs := range fr.subs {
		for sub := range subs {
			c.send("CLOSED", sub, "error: shutting down")
		}
	}
}

func (fr *fakeRelay) count() int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.published
}
