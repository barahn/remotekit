// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package rendezvous lets two peers that have never met find each other by a
// short code, or by a link that mints one.
//
// It is mechanism, not policy. A Registry allocates codes, issues and revokes
// links, redeems codes, and throttles failed attempts per origin. It has no
// notion of a user, a role, consent, or who controls whom, and it chooses no
// values: code lifetimes, link lifetimes and throttle limits are all the
// caller's. What happens once two peers are matched -- signalling, consent,
// a session -- is the caller's too.
//
// Three rules shape it, each learnt the hard way in a consumer:
//
//   - Every entry point that takes a code or a token from the network is
//     metered. An unmetered lookup is an oracle, and with one the 900,000-code
//     keyspace can be swept however tight the limit elsewhere is. Redeem and
//     MintFromLink share one per-origin budget, so alternating between them
//     buys nothing.
//   - Codes are single-use. A code that has done its job opens nothing, so one
//     that leaks afterwards is harmless. A link is the durable capability; each
//     use of it mints a fresh single-use code.
//   - One mutex and no goroutines. Expiry is checked on access, and Sweep
//     reclaims memory when the caller chooses to call it. There is no
//     background reaper to deadlock against, and no second lock to take in
//     the wrong order.
//
// Redeem distinguishes ErrNotFound from ErrExpired, and both cost the caller
// one attempt. A consumer answering over a network should not pass that
// distinction on: telling a guesser which codes once existed narrows the
// search.
package rendezvous

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PeerID identifies a peer to the Registry. What it means is the caller's
// business; the Registry only returns it.
type PeerID string

// Token is a link's capability: whoever holds it can mint codes for the link's
// owner. 256 bits from crypto/rand, URL-safe.
type Token string

// Limits bounds failed attempts per origin. Both fields must be positive: the
// mechanism picks no default, because what is tight enough depends on who is
// behind one origin -- one person, or an office behind one NAT.
type Limits struct {
	MaxFailures int
	Window      time.Duration
}

// Match is the result of a successful redemption: the peer the code belonged
// to, and the code that was consumed.
type Match struct {
	Owner PeerID
	Code  Code
}

// Stats counts what a Registry is holding, for metrics and for checking that
// Sweep is being called often enough.
type Stats struct {
	Codes   int // live and not-yet-swept expired codes
	Links   int // links not revoked or released
	Origins int // origins with failures in an open or unswept window
}

var (
	ErrNotFound  = errors.New("rendezvous: no such code or link")
	ErrExpired   = errors.New("rendezvous: code has expired")
	ErrThrottled = errors.New("rendezvous: too many failed attempts from this origin; try again later")
	ErrExhausted = errors.New("rendezvous: could not allocate an unused code")
	ErrLimits    = errors.New("rendezvous: limits must be positive")
	ErrTTL       = errors.New("rendezvous: a code's lifetime must be positive")
)

// maxAllocAttempts bounds the search for an unused code. A collision is only
// likely once a large share of the 900,000 codes is live at once, and at that
// point failing loudly is better than looping.
const maxAllocAttempts = 16

type codeEntry struct {
	owner   PeerID
	expires time.Time
}

type linkEntry struct {
	owner   PeerID
	expires time.Time // zero: until revoked or released
}

// Registry matches peers by code. Its zero value is not usable; call New.
type Registry struct {
	mu    sync.Mutex
	now   func() time.Time
	codes map[Code]codeEntry
	links map[Token]linkEntry
	thr   *throttle
}

// Option configures a Registry.
type Option func(*Registry)

// WithClock replaces the Registry's clock. Tests use it to move time without
// sleeping; nothing else should need it.
func WithClock(now func() time.Time) Option {
	return func(r *Registry) { r.now = now }
}

// New returns a Registry that throttles failed attempts by l.
func New(l Limits, opts ...Option) (*Registry, error) {
	if l.MaxFailures <= 0 || l.Window <= 0 {
		return nil, ErrLimits
	}
	r := &Registry{
		now:   time.Now,
		codes: make(map[Code]codeEntry),
		links: make(map[Token]linkEntry),
		thr:   newThrottle(l.MaxFailures, l.Window),
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// SetLimits replaces the throttle's limits and forgets the failures counted so
// far.
func (r *Registry) SetLimits(l Limits) error {
	if l.MaxFailures <= 0 || l.Window <= 0 {
		return ErrLimits
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.thr = newThrottle(l.MaxFailures, l.Window)
	return nil
}

// Allocate returns a single-use code by which owner can be reached, valid for
// ttl. It is drawn from crypto/rand and unused among live codes.
func (r *Registry) Allocate(owner PeerID, ttl time.Duration) (Code, error) {
	if ttl <= 0 {
		return "", ErrTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allocateLocked(owner, ttl, r.now())
}

func (r *Registry) allocateLocked(owner PeerID, ttl time.Duration, now time.Time) (Code, error) {
	for i := 0; i < maxAllocAttempts; i++ {
		c, err := newCode()
		if err != nil {
			return "", err
		}
		if e, taken := r.codes[c]; taken && now.Before(e.expires) {
			continue
		}
		r.codes[c] = codeEntry{owner: owner, expires: now.Add(ttl)}
		return c, nil
	}
	return "", ErrExhausted
}

// Redeem consumes a code presented by a caller at origin and returns whose it
// was. input may carry the separators people type (spaces, hyphens, dots).
//
// Every outcome other than success costs origin one attempt -- a malformed
// code, an unknown one, an expired one -- and an origin out of attempts is
// refused even when its next guess is a live code; otherwise a throttled
// guesser still wins the moment it guesses right. Success clears the origin.
func (r *Registry) Redeem(input, origin string) (Match, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()

	if !r.thr.allow(origin, now) {
		return Match{}, ErrThrottled
	}
	c, err := Normalize(input)
	if err != nil {
		r.thr.fail(origin, now)
		return Match{}, err
	}
	e, ok := r.codes[c]
	if !ok {
		r.thr.fail(origin, now)
		return Match{}, ErrNotFound
	}
	delete(r.codes, c) // single-use, and an expired code is gone either way
	if !now.Before(e.expires) {
		r.thr.fail(origin, now)
		return Match{}, ErrExpired
	}
	r.thr.clear(origin)
	return Match{Owner: e.owner, Code: c}, nil
}

// IssueLink returns a token from which codes for owner can be minted. A ttl of
// zero means the link lives until RevokeLink or Release; a positive ttl bounds
// it as well.
func (r *Registry) IssueLink(owner PeerID, ttl time.Duration) (Token, error) {
	if ttl < 0 {
		return "", ErrTTL
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rendezvous: drawing a link token: %w", err)
	}
	t := Token(base64.RawURLEncoding.EncodeToString(b[:]))

	r.mu.Lock()
	defer r.mu.Unlock()
	var expires time.Time
	if ttl > 0 {
		expires = r.now().Add(ttl)
	}
	r.links[t] = linkEntry{owner: owner, expires: expires}
	return t, nil
}

// MintFromLink creates a fresh single-use code for the link's owner, valid for
// codeTTL -- one per connection, so a code that leaks after use opens nothing.
//
// It is metered against origin exactly like Redeem, from the same budget: an
// unknown, revoked or expired token costs one attempt.
func (r *Registry) MintFromLink(t Token, origin string, codeTTL time.Duration) (Code, error) {
	if codeTTL <= 0 {
		return "", ErrTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()

	if !r.thr.allow(origin, now) {
		return "", ErrThrottled
	}
	l, ok := r.links[t]
	if !ok {
		r.thr.fail(origin, now)
		return "", ErrNotFound
	}
	if !l.expires.IsZero() && !now.Before(l.expires) {
		delete(r.links, t)
		r.thr.fail(origin, now)
		return "", ErrNotFound
	}
	c, err := r.allocateLocked(l.owner, codeTTL, now)
	if err != nil {
		return "", err
	}
	r.thr.clear(origin)
	return c, nil
}

// RevokeLink stops a link minting codes. Codes it already minted stay valid
// until used or expired; revoke those with Release if they must go too.
// Revoking an unknown token is not an error.
func (r *Registry) RevokeLink(t Token) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.links, t)
}

// Release forgets everything owned by owner -- its codes and its links -- for
// when the owner goes away. A code or link that belonged to it is unknown
// afterwards.
func (r *Registry) Release(owner PeerID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c, e := range r.codes {
		if e.owner == owner {
			delete(r.codes, c)
		}
	}
	for t, l := range r.links {
		if l.owner == owner {
			delete(r.links, t)
		}
	}
}

// Sweep reclaims expired codes and links and closed throttle windows. Expiry is
// enforced on access whether or not Sweep runs; Sweep only bounds memory. Call
// it periodically.
func (r *Registry) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for c, e := range r.codes {
		if !now.Before(e.expires) {
			delete(r.codes, c)
		}
	}
	for t, l := range r.links {
		if !l.expires.IsZero() && !now.Before(l.expires) {
			delete(r.links, t)
		}
	}
	r.thr.sweep(now)
}

// Stats reports what the Registry is holding.
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{Codes: len(r.codes), Links: len(r.links), Origins: len(r.thr.byOrig)}
}
