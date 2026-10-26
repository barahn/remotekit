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
	// DefaultRelayGracePeriod is used when Config.RelayGracePeriod is zero.
	DefaultRelayGracePeriod = 30 * time.Second
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
	// ErrAuthRefused is returned when a relay refuses NIP-42 authentication.
	ErrAuthRefused = errors.New("nostr: relay authentication refused")

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
	// RelayAuthKeys maps a relay URL to a specific SecretKey used to
	// authenticate via NIP-42 when challenged by that relay. If a relay is
	// not in this map or if it is nil, the session's own key is used.
	RelayAuthKeys map[string]*SecretKey
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
	// RelayGracePeriod bounds how long the Conn will survive when all relays
	// are unreachable. If at least one relay reconnects within this period,
	// the session continues. Zero means DefaultRelayGracePeriod.
	RelayGracePeriod time.Duration
}

// Conn is a signalling connection over Nostr relays. It satisfies
// stream.SignalConn.
type Conn struct {
	key           *SecretKey
	peer          string // Dial's peer; "" on a Listen conn
	ttl           time.Duration
	maxViewers    int
	dialer        *websocket.Dialer
	subID         string
	gracePeriod   time.Duration
	relayAuthKeys map[string]*SecretKey

	mu           sync.Mutex
	relayCond    *sync.Cond
	activeRelays map[string]*relay
	seen         map[string]time.Time
	viewers      map[string]struct{}
	alive        int
	graceTimer   *time.Timer
	closed       bool
	err          error

	in        chan []byte
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
		key:           key,
		peer:          peer,
		ttl:           cfg.MessageTTL,
		maxViewers:    cfg.MaxViewers,
		dialer:        dialer,
		gracePeriod:   cfg.RelayGracePeriod,
		activeRelays:  make(map[string]*relay),
		in:            make(chan []byte, 64),
		seen:          map[string]time.Time{},
		viewers:       map[string]struct{}{},
		relayAuthKeys: cfg.RelayAuthKeys,
		done:          make(chan struct{}),
	}
	c.relayCond = sync.NewCond(&c.mu)
	if c.ttl <= 0 {
		c.ttl = DefaultMessageTTL
	}
	if c.maxViewers <= 0 {
		c.maxViewers = DefaultMaxViewers
	}
	if c.gracePeriod <= 0 {
		c.gracePeriod = DefaultRelayGracePeriod
	}

	subID, err := randomID()
	if err != nil {
		return nil, err
	}
	c.subID = subID

	// Seals and wraps are backdated by up to wrapJitter, and a filter
	// matches on the wrap's created_at, so the window reaches that far back.
	// Older traffic still delivered is dropped by the rumor's own timestamp.
	initialSince := time.Now().Add(-wrapJitter - time.Minute).Unix()
	f := filter{
		Kinds: []int{KindGiftWrap},
		P:     []string{key.PublicKey()},
		Since: initialSince,
	}

	type initialDial struct {
		url string
		r   *relay
	}
	var dialed []initialDial
	for _, url := range cfg.Relays {
		r, err := dialRelay(ctx, dialer, url, c.authKeyFor(url))
		if err != nil {
			continue
		}
		if err := r.subscribe(subID, f); err != nil {
			_ = r.close()
			continue
		}
		dialed = append(dialed, initialDial{url: url, r: r})
	}
	if len(dialed) == 0 {
		return nil, ErrNoRelays
	}

	c.mu.Lock()
	for _, d := range dialed {
		c.activeRelays[d.url] = d.r
	}
	c.alive = len(c.activeRelays)
	c.mu.Unlock()

	for _, url := range cfg.Relays {
		var initR *relay
		for _, d := range dialed {
			if d.url == url {
				initR = d.r
				break
			}
		}
		go c.relayWorker(url, initR, initialSince)
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

func (c *Conn) relayWorker(url string, initialR *relay, initialSince int64) {
	lastActive := time.Now()
	r := initialR
	backoff := 50 * time.Millisecond
	const maxBackoff = 3 * time.Second

	for {
		if r == nil {
			select {
			case <-c.done:
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, maxBackoff)
			}

			select {
			case <-c.done:
				return
			default:
			}

			dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			newR, err := dialRelay(dialCtx, c.dialer, url, c.authKeyFor(url))
			cancel()
			if err != nil {
				if errors.Is(err, ErrAuthRefused) {
					c.mu.Lock()
					if c.err == nil {
						c.err = err
					}
					c.mu.Unlock()
					_ = c.Close()
					return
				}
				continue
			}

			subSince := lastActive.Add(-wrapJitter - time.Minute).Unix()
			f := filter{
				Kinds: []int{KindGiftWrap},
				P:     []string{c.key.PublicKey()},
				Since: subSince,
			}
			if err := newR.subscribe(c.subID, f); err != nil {
				_ = newR.close()
				continue
			}

			r = newR
			backoff = 50 * time.Millisecond
			c.setRelayActive(url, r)
		}

		readErr := r.read(c.subID, func(ev *Event) {
			lastActive = time.Now()
			c.receive(ev)
		})
		if errors.Is(readErr, ErrAuthRefused) {
			c.mu.Lock()
			if c.err == nil {
				c.err = readErr
			}
			c.mu.Unlock()
			_ = r.close()
			c.setRelayInactive(url, r)
			_ = c.Close()
			return
		}

		_ = r.close()
		lastActive = time.Now()
		c.setRelayInactive(url, r)
		r = nil

		select {
		case <-c.done:
			return
		default:
		}
	}
}

func (c *Conn) setRelayActive(url string, r *relay) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = r.close()
		return
	}
	c.activeRelays[url] = r
	c.alive = len(c.activeRelays)
	if c.graceTimer != nil {
		c.graceTimer.Stop()
		c.graceTimer = nil
	}
	c.relayCond.Broadcast()
}

func (c *Conn) setRelayInactive(url string, oldR *relay) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeRelays[url] == oldR {
		delete(c.activeRelays, url)
		c.alive = len(c.activeRelays)
	}
	if c.alive <= 0 && !c.closed {
		if c.graceTimer == nil {
			c.graceTimer = time.AfterFunc(c.gracePeriod, func() {
				_ = c.Close()
			})
		}
	}
}

func (c *Conn) authKeyFor(url string) *SecretKey {
	if c.relayAuthKeys != nil {
		if k, ok := c.relayAuthKeys[url]; ok && k != nil {
			return k
		}
	}
	return c.key
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
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrClosed
	}
}

// Err returns the error that caused the Conn to close, if any, or nil.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// WriteMessage gift-wraps data for its recipients and publishes it to every
// relay. It succeeds if at least one relay took it; a relay that fails is
// dropped.
func (c *Conn) WriteMessage(data []byte) error {
	select {
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return err
		}
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
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return ErrClosed
		}
		if len(c.activeRelays) == 0 {
			c.relayCond.Wait()
			if c.closed {
				c.mu.Unlock()
				return ErrClosed
			}
		}
		relays := make([]*relay, 0, len(c.activeRelays))
		for _, r := range c.activeRelays {
			relays = append(relays, r)
		}
		c.mu.Unlock()

		sent := false
		for _, r := range relays {
			if err := r.publish(ev); err != nil {
				_ = r.close()
				continue
			}
			sent = true
		}
		if sent {
			return nil
		}
		select {
		case <-c.done:
			return ErrClosed
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Close closes every relay connection and unblocks ReadMessage. It may be
// called more than once.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.graceTimer != nil {
			c.graceTimer.Stop()
			c.graceTimer = nil
		}
		for _, r := range c.activeRelays {
			_ = r.close()
		}
		c.activeRelays = nil
		c.relayCond.Broadcast()
		c.mu.Unlock()
		close(c.done)
	})
	return nil
}
