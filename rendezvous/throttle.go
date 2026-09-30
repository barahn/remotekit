// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package rendezvous

import "time"

// unknownOrigin is the bucket for callers that supply no origin. It is shared,
// so origin-less callers are throttled together. Skipping the limit for an
// empty origin instead would make the whole defence opt-out -- the one thing an
// attacker would do. A consumer that forgets to pass the client's address
// therefore throttles its own users, a loud failure rather than a silent hole.
const unknownOrigin = "\x00unknown"

// attempts counts one origin's failures in the current window.
type attempts struct {
	failures int
	since    time.Time
}

// throttle bounds how many failed attempts one origin may make per window.
//
// It is a fixed window: an attacker gains at most one extra burst at a window
// boundary, which does not change the order of magnitude of the keyspace they
// can sweep, and the bookkeeping stays off an honest caller's path.
//
// It has no lock of its own. Registry holds its single mutex around every
// call, so there is no second lock to take in the wrong order.
type throttle struct {
	max    int
	window time.Duration
	byOrig map[string]*attempts
}

func newThrottle(max int, window time.Duration) *throttle {
	return &throttle{max: max, window: window, byOrig: make(map[string]*attempts)}
}

func originKey(origin string) string {
	if origin == "" {
		return unknownOrigin
	}
	return origin
}

func (t *throttle) allow(origin string, now time.Time) bool {
	a, ok := t.byOrig[originKey(origin)]
	if !ok || now.Sub(a.since) >= t.window {
		return true
	}
	return a.failures < t.max
}

func (t *throttle) fail(origin string, now time.Time) {
	key := originKey(origin)
	a, ok := t.byOrig[key]
	if !ok || now.Sub(a.since) >= t.window {
		t.byOrig[key] = &attempts{failures: 1, since: now}
		return
	}
	a.failures++
}

// clear forgets an origin after it succeeds, so someone who mistypes a code
// once and then gets it right starts clean.
func (t *throttle) clear(origin string) {
	delete(t.byOrig, originKey(origin))
}

// sweep drops closed windows, so an attacker cycling origins cannot grow the
// map without bound.
func (t *throttle) sweep(now time.Time) {
	for key, a := range t.byOrig {
		if now.Sub(a.since) >= t.window {
			delete(t.byOrig, key)
		}
	}
}
