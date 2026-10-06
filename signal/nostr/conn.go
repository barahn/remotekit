// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package nostr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// DefaultMessageTTL is used when Config.MessageTTL is zero.
	DefaultMessageTTL = 5 * time.Minute
	// DefaultMaxViewers is used when Config.MaxViewers is zero.
	DefaultMaxViewers = 8
)

var (
	// ErrClosed is returned once a Conn has been closed, or has lost every
	// relay.
	ErrClosed = errors.New("nostr: connection closed")
	// ErrNoRelays is returned by Listen and Dial when no relay could be
	// reached.
	ErrNoRelays = errors.New("nostr: no relay reachable")
	// ErrUnknownViewer is returned for a message addressed to a viewer this
	// Conn has not heard from.
	ErrUnknownViewer = errors.New("nostr: message addressed to an unknown viewer")

	errNotObject = errors.New("nostr: signalling message is not a JSON object")
)

// Config says which relays to use and under which key.
type Config struct {
	// Relays are the relay URLs (wss://...), all of which are used: every
	// message is published to each, and read from whichever delivers first.
	Relays []string
	// Key is the key this end is addressed by. Leave it nil to have one
	// generated, which is what a session should do; see the package
	// documentation on keys.
	Key *SecretKey
	// Dialer dials the relays; nil means websocket.DefaultDialer.
	Dialer *websocket.Dialer
	// MessageTTL bounds how long a message lives: each gift wrap asks
	// relays (NIP-40) to drop it after this long, and a message written
	// longer ago than this, or as far in the future, is discarded on
	// receipt. Zero means DefaultMessageTTL.
	MessageTTL time.Duration
	// MaxViewers caps how many senders a Listen conn will talk to. Every
	// message the agent broadcasts is wrapped once per viewer, so this
	// bounds what a flood of new keys can make it publish. Zero means
	// DefaultMaxViewers. Dial ignores it.
	MaxViewers int
}

// Conn is a signalling connection over Nostr relays. It satisfies
// tunnel.SignalConn.
type Conn struct {
	key        *SecretKey
	peer       string // Dial's peer; "" on a Listen conn
	ttl        time.Duration
	maxViewers int
	relays     []*relay
	in         chan []byte

	mu      sync.Mutex
	seen    map[string]time.Time
	viewers map[string]struct{}
	alive   int

	done      chan struct{}
	closeOnce sync.Once
}

// Listen opens the agent's end: it accepts messages from any sender, up to
// MaxViewers of them, and labels each with its sender as viewer_id, the field
// the control plane sets over the WebSocket. Whatever viewer_id a message
// itself carried is overwritten, so one viewer cannot pose as another. The
// agent's replies go back to the viewer named in their viewer_id, and those
// without one go to every viewer heard from so far.
//
// ctx bounds only the dialling; the Conn lives until Close.
func Listen(ctx context.Context, cfg Config) (*Conn, error) {
	return open(ctx, cfg, "")
}

// Dial opens a viewer's end, talking to the one agent whose session key is
// peer: messages go only to peer, and only peer's are read.
//
// ctx bounds only the dialling; the Conn lives until Close.
func Dial(ctx context.Context, cfg Config, peer string) (*Conn, error) {
	if !ValidPublicKey(peer) {
		return nil, ErrInvalidKey
	}
	return open(ctx, cfg, peer)
}

func open(ctx context.Context, cfg Config, peer string) (*Conn, error) {
	key := cfg.Key
	if key == nil {
		var err error
		if key, err = GenerateKey(); err != nil {
			return nil, err
		}
	}
	dialer := cfg.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	c := &Conn{
		key:        key,
		peer:       peer,
		ttl:        cfg.MessageTTL,
		maxViewers: cfg.MaxViewers,
		in:         make(chan []byte, 64),
		seen:       map[string]time.Time{},
		viewers:    map[string]struct{}{},
		done:       make(chan struct{}),
	}
	if c.ttl <= 0 {
		c.ttl = DefaultMessageTTL
	}
	if c.maxViewers <= 0 {
		c.maxViewers = DefaultMaxViewers
	}

	subID, err := randomID()
	if err != nil {
		return nil, err
	}
	// Seals and wraps are backdated by up to wrapJitter, and a filter
	// matches on the wrap's created_at, so the window reaches that far back.
	// Older traffic still delivered is dropped by the rumor's own timestamp.
	f := filter{
		Kinds: []int{KindGiftWrap},
		P:     []string{key.PublicKey()},
		Since: time.Now().Add(-wrapJitter - time.Minute).Unix(),
	}

	for _, url := range cfg.Relays {
		r, err := dialRelay(ctx, dialer, url)
		if err != nil {
			continue
		}
		if err := r.subscribe(subID, f); err != nil {
			_ = r.close()
			continue
		}
		c.relays = append(c.relays, r)
	}
	if len(c.relays) == 0 {
		return nil, ErrNoRelays
	}
	c.alive = len(c.relays)
	for _, r := range c.relays {
		go c.readRelay(r, subID)
	}
	return c, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// PublicKey is the key this end is addressed by: what a viewer must be given
// to Dial an agent's Listen conn.
func (c *Conn) PublicKey() string { return c.key.PublicKey() }

func (c *Conn) readRelay(r *relay, subID string) {
	_ = r.read(subID, c.receive)
	_ = r.close()
	c.mu.Lock()
	c.alive--
	last := c.alive == 0
	c.mu.Unlock()
	if last {
		_ = c.Close()
	}
}

// firstSight records id and reports whether it is new. Entries are kept for
// twice the TTL: anything older fails the freshness check anyway.
func (c *Conn) firstSight(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[id]; ok {
		return false
	}
	for k, at := range c.seen {
		if now.Sub(at) > 2*c.ttl {
			delete(c.seen, k)
		}
	}
	c.seen[id] = now
	return true
}

// receive handles one event from any relay. The same wrap arrives once per
// relay, and only the first is delivered.
func (c *Conn) receive(ev *Event) {
	now := time.Now()
	if !c.firstSight("w:"+ev.ID, now) {
		return
	}
	sender, content, err := unwrap(ev, c.key, now, c.ttl)
	if err != nil {
		return
	}
	msg, ok := c.admit(sender, []byte(content))
	if !ok {
		return
	}
	select {
	case c.in <- msg:
	case <-c.done:
	}
}

// admit decides whether a message from sender is delivered, and labels it.
func (c *Conn) admit(sender string, msg []byte) ([]byte, bool) {
	if c.peer != "" {
		return msg, sender == c.peer
	}
	c.mu.Lock()
	_, known := c.viewers[sender]
	if !known {
		if len(c.viewers) >= c.maxViewers {
			c.mu.Unlock()
			return nil, false
		}
		c.viewers[sender] = struct{}{}
	}
	c.mu.Unlock()

	labelled, err := setViewerID(msg, sender)
	if err != nil {
		return nil, false
	}
	return labelled, true
}

func setViewerID(msg []byte, viewer string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(msg, &fields); err != nil || fields == nil {
		return nil, errNotObject
	}
	id, err := json.Marshal(viewer)
	if err != nil {
		return nil, err
	}
	fields["viewer_id"] = id
	return json.Marshal(fields)
}

// ReadMessage returns the next message, or ErrClosed once the Conn is closed
// or has lost every relay.
func (c *Conn) ReadMessage() ([]byte, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.done:
		return nil, ErrClosed
	}
}

// WriteMessage gift-wraps data for its recipients and publishes it to every
// relay. It succeeds if at least one relay took it; a relay that fails is
// dropped.
func (c *Conn) WriteMessage(data []byte) error {
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	recipients, err := c.recipients(data)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, to := range recipients {
		gift, err := wrap(string(data), c.key, to, now, c.ttl)
		if err != nil {
			return err
		}
		if err := c.publish(gift); err != nil {
			return err
		}
	}
	return nil
}

// recipients is who a message goes to: Dial's peer, or on a Listen conn the
// viewer in viewer_id, or every viewer when it names none.
func (c *Conn) recipients(data []byte) ([]string, error) {
	if c.peer != "" {
		return []string{c.peer}, nil
	}
	var addr struct {
		ViewerID string `json:"viewer_id"`
	}
	if err := json.Unmarshal(data, &addr); err != nil {
		return nil, errNotObject
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if addr.ViewerID != "" {
		if _, ok := c.viewers[addr.ViewerID]; !ok {
			return nil, ErrUnknownViewer
		}
		return []string{addr.ViewerID}, nil
	}
	all := make([]string, 0, len(c.viewers))
	for v := range c.viewers {
		all = append(all, v)
	}
	return all, nil
}

func (c *Conn) publish(ev *Event) error {
	sent := false
	for _, r := range c.relays {
		if err := r.publish(ev); err != nil {
			// Closing it ends its read loop, which retires it.
			_ = r.close()
			continue
		}
		sent = true
	}
	if !sent {
		return ErrClosed
	}
	return nil
}

// Close closes every relay connection and unblocks ReadMessage. It may be
// called more than once.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		for _, r := range c.relays {
			_ = r.close()
		}
	})
	return nil
}
