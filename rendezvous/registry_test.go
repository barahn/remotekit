// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package rendezvous_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/barahn/remotekit/rendezvous"
)

// clock is a settable time source, so expiry is tested by moving time rather
// than by sleeping -- sleeps are what make timing tests flaky under load.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newRegistry(t *testing.T, max int, window time.Duration) (*rendezvous.Registry, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r, err := rendezvous.New(rendezvous.Limits{MaxFailures: max, Window: window}, rendezvous.WithClock(c.now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, c
}

func TestNewRefusesNonPositiveLimits(t *testing.T) {
	for _, l := range []rendezvous.Limits{{}, {MaxFailures: 5}, {Window: time.Minute}, {MaxFailures: -1, Window: time.Minute}} {
		if _, err := rendezvous.New(l); !errors.Is(err, rendezvous.ErrLimits) {
			t.Errorf("New(%+v) = %v, want ErrLimits", l, err)
		}
	}
}

func TestRedeemReturnsTheOwner(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	c, err := r.Allocate("peer-2", time.Minute)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if len(c) != rendezvous.CodeLength || c < "100000" || c > "999999" {
		t.Fatalf("code %q is not six digits from 100000", c)
	}
	// Redeemed the way a person reads it out.
	m, err := r.Redeem(c.Format(), "198.51.100.1")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if m.Owner != "peer-2" || m.Code != c {
		t.Fatalf("Match = %+v", m)
	}
}

// A code that has done its job opens nothing, so one that leaks afterwards is
// harmless.
func TestCodesAreSingleUse(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	c, _ := r.Allocate("peer-2", time.Minute)
	if _, err := r.Redeem(string(c), "198.51.100.1"); err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	if _, err := r.Redeem(string(c), "198.51.100.2"); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("second Redeem = %v, want ErrNotFound", err)
	}
}

func TestAllocateRequiresALifetime(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	if _, err := r.Allocate("p", 0); !errors.Is(err, rendezvous.ErrTTL) {
		t.Errorf("Allocate with no ttl = %v, want ErrTTL", err)
	}
}

func TestAllocatedCodesAreDistinct(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	seen := make(map[rendezvous.Code]bool)
	for i := 0; i < 5000; i++ {
		c, err := r.Allocate(rendezvous.PeerID(fmt.Sprint(i)), time.Hour)
		if err != nil {
			t.Fatalf("Allocate %d: %v", i, err)
		}
		if seen[c] {
			t.Fatalf("code %s allocated twice while live", c)
		}
		seen[c] = true
	}
}

// An expired code costs an attempt like a wrong one, so watching the budget
// does not tell a caller which codes once existed.
func TestExpiredCodesFailAndCount(t *testing.T) {
	r, clk := newRegistry(t, 1, time.Hour)
	c, _ := r.Allocate("peer-2", time.Minute)
	clk.advance(time.Minute)

	if _, err := r.Redeem(string(c), "192.0.2.1"); !errors.Is(err, rendezvous.ErrExpired) {
		t.Fatalf("Redeem after expiry = %v, want ErrExpired", err)
	}
	if _, err := r.Redeem("000000", "192.0.2.1"); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("an expired code was free to probe: %v", err)
	}
}

// The PIN-style code is the only credential, so an origin that keeps guessing
// wrong must be cut off -- and stay cut off even when its next guess is live,
// or a throttled guesser still wins the moment it guesses right.
func TestWrongGuessesAreThrottled(t *testing.T) {
	r, _ := newRegistry(t, 3, time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := r.Redeem("000000", "203.0.113.7"); !errors.Is(err, rendezvous.ErrNotFound) {
			t.Fatalf("guess %d = %v, want ErrNotFound", i+1, err)
		}
	}
	live, _ := r.Allocate("peer-2", time.Minute)
	if _, err := r.Redeem(string(live), "203.0.113.7"); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("a throttled origin redeemed a live code: %v", err)
	}
	// And the live code was not consumed by the refused attempt.
	if _, err := r.Redeem(string(live), "198.51.100.9"); err != nil {
		t.Fatalf("the refused attempt spent the code: %v", err)
	}
}

func TestThrottleIsPerOrigin(t *testing.T) {
	r, _ := newRegistry(t, 2, time.Minute)
	for i := 0; i < 2; i++ {
		_, _ = r.Redeem("000000", "198.51.100.1")
	}
	c, _ := r.Allocate("peer-2", time.Minute)
	if _, err := r.Redeem(string(c), "198.51.100.2"); err != nil {
		t.Fatalf("one origin's failures throttled another: %v", err)
	}
}

// A success does not refund failures. If it did, anyone holding a code of
// their own -- and a link mints one on demand -- could reset their budget
// after every few guesses and sweep the keyspace unthrottled.
func TestSuccessDoesNotResetTheBudget(t *testing.T) {
	r, c := newRegistry(t, 3, time.Minute)
	link, err := r.IssueLink("attacker", 0)
	if err != nil {
		t.Fatal(err)
	}
	const origin = "192.0.2.50"
	for i := 0; i < 2; i++ {
		_, _ = r.Redeem("000000", origin)
	}
	own, err := r.MintFromLink(link, origin, time.Minute)
	if err != nil {
		t.Fatalf("minting from one's own link: %v", err)
	}
	if _, err := r.Redeem(string(own), origin); err != nil {
		t.Fatalf("redeeming one's own code: %v", err)
	}
	if _, err := r.Redeem("000000", origin); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("third failure = %v, want ErrNotFound", err)
	}
	if _, err := r.Redeem("000000", origin); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("after three failures and a success in between = %v, want ErrThrottled", err)
	}

	// The budget comes back when the window closes, and only then.
	c.advance(time.Minute)
	if _, err := r.Redeem("000000", origin); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("after the window = %v, want ErrNotFound", err)
	}
}

// A malformed code is a guess too; leaving it uncounted would be an
// unmetered probe.
func TestMalformedInputCounts(t *testing.T) {
	r, _ := newRegistry(t, 2, time.Minute)
	for i := 0; i < 2; i++ {
		if _, err := r.Redeem("abc", "203.0.113.99"); !errors.Is(err, rendezvous.ErrMalformed) {
			t.Fatalf("Redeem(abc) = %v, want ErrMalformed", err)
		}
	}
	if _, err := r.Redeem("abc", "203.0.113.99"); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("malformed guesses were not counted: %v", err)
	}
}

// Callers without an origin share one bucket rather than escaping the limit.
func TestMissingOriginIsNotAnEscapeHatch(t *testing.T) {
	r, _ := newRegistry(t, 2, time.Minute)
	for i := 0; i < 2; i++ {
		_, _ = r.Redeem("000000", "")
	}
	if _, err := r.Redeem("000000", ""); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("origin-less callers escaped the throttle: %v", err)
	}
}

func TestTheWindowReopens(t *testing.T) {
	r, clk := newRegistry(t, 1, time.Minute)
	_, _ = r.Redeem("000000", "192.0.2.77")
	if _, err := r.Redeem("000000", "192.0.2.77"); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatal("expected the origin to be throttled")
	}
	clk.advance(time.Minute)
	c, _ := r.Allocate("peer-2", time.Hour)
	if _, err := r.Redeem(string(c), "192.0.2.77"); err != nil {
		t.Fatalf("the window did not reopen: %v", err)
	}
}

// Each use of a link mints a fresh single-use code for the link's owner.
func TestLinkMintsAFreshCodePerUse(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	tok, err := r.IssueLink("peer-2", 0)
	if err != nil {
		t.Fatalf("IssueLink: %v", err)
	}
	a, err := r.MintFromLink(tok, "198.51.100.1", time.Minute)
	if err != nil {
		t.Fatalf("first mint: %v", err)
	}
	b, err := r.MintFromLink(tok, "198.51.100.2", time.Minute)
	if err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if a == b {
		t.Fatal("two uses of a link minted the same code")
	}
	for _, c := range []rendezvous.Code{a, b} {
		m, err := r.Redeem(string(c), "198.51.100.3")
		if err != nil || m.Owner != "peer-2" {
			t.Fatalf("minted code %s: %+v, %v", c, m, err)
		}
	}
}

func TestRevokedLinkMintsNothing(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	tok, _ := r.IssueLink("peer-2", 0)
	r.RevokeLink(tok)
	if _, err := r.MintFromLink(tok, "198.51.100.1", time.Minute); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("mint from a revoked link = %v, want ErrNotFound", err)
	}
	r.RevokeLink(tok) // idempotent
}

func TestLinkLifetime(t *testing.T) {
	r, clk := newRegistry(t, 5, time.Minute)
	bounded, _ := r.IssueLink("peer-2", time.Hour)
	forever, _ := r.IssueLink("peer-2", 0)
	clk.advance(time.Hour)

	if _, err := r.MintFromLink(bounded, "198.51.100.1", time.Minute); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("an expired link minted: %v", err)
	}
	if _, err := r.MintFromLink(forever, "198.51.100.1", time.Minute); err != nil {
		t.Fatalf("a link with no ttl expired: %v", err)
	}
	if _, err := r.IssueLink("peer-2", -time.Second); !errors.Is(err, rendezvous.ErrTTL) {
		t.Fatalf("negative link ttl = %v, want ErrTTL", err)
	}
}

// Redeem and MintFromLink draw on one budget, or a guesser simply alternates.
func TestLinkGuessesShareTheRedeemBudget(t *testing.T) {
	r, _ := newRegistry(t, 2, time.Minute)
	_, _ = r.MintFromLink("not-a-token", "203.0.113.5", time.Minute)
	_, _ = r.Redeem("000000", "203.0.113.5")

	tok, _ := r.IssueLink("peer-2", 0)
	if _, err := r.MintFromLink(tok, "203.0.113.5", time.Minute); !errors.Is(err, rendezvous.ErrThrottled) {
		t.Fatalf("mixed guesses escaped the shared budget: %v", err)
	}
}

// When an owner goes away, everything that could reach it goes too.
func TestReleaseForgetsTheOwner(t *testing.T) {
	r, _ := newRegistry(t, 5, time.Minute)
	c, _ := r.Allocate("peer-2", time.Minute)
	tok, _ := r.IssueLink("peer-2", 0)
	minted, _ := r.MintFromLink(tok, "198.51.100.1", time.Minute)
	other, _ := r.Allocate("peer-3", time.Minute)

	r.Release("peer-2")

	for _, code := range []rendezvous.Code{c, minted} {
		if _, err := r.Redeem(string(code), "198.51.100.9"); !errors.Is(err, rendezvous.ErrNotFound) {
			t.Errorf("released owner's code %s = %v, want ErrNotFound", code, err)
		}
	}
	if _, err := r.MintFromLink(tok, "198.51.100.8", time.Minute); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Errorf("released owner's link still mints: %v", err)
	}
	if m, err := r.Redeem(string(other), "198.51.100.7"); err != nil || m.Owner != "peer-3" {
		t.Errorf("Release touched another owner: %+v, %v", m, err)
	}
}

func TestSweepReclaimsWhatExpired(t *testing.T) {
	r, clk := newRegistry(t, 5, time.Minute)
	_, _ = r.Allocate("peer-2", time.Minute)
	_, _ = r.IssueLink("peer-2", time.Minute)
	_, _ = r.IssueLink("peer-3", 0)
	_, _ = r.Redeem("000000", "203.0.113.1")

	if s := r.Stats(); s.Codes != 1 || s.Links != 2 || s.Origins != 1 {
		t.Fatalf("before sweep: %+v", s)
	}
	clk.advance(time.Minute)
	r.Sweep()
	if s := r.Stats(); s.Codes != 0 || s.Links != 1 || s.Origins != 0 {
		t.Fatalf("after sweep: %+v, want only the unbounded link left", s)
	}
}

func TestSetLimitsReplacesTheBudget(t *testing.T) {
	r, _ := newRegistry(t, 1, time.Minute)
	_, _ = r.Redeem("000000", "192.0.2.9")
	if err := r.SetLimits(rendezvous.Limits{MaxFailures: 3, Window: time.Minute}); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}
	if _, err := r.Redeem("000000", "192.0.2.9"); !errors.Is(err, rendezvous.ErrNotFound) {
		t.Fatalf("counts survived SetLimits: %v", err)
	}
	if err := r.SetLimits(rendezvous.Limits{}); !errors.Is(err, rendezvous.ErrLimits) {
		t.Fatalf("SetLimits({}) = %v, want ErrLimits", err)
	}
}

// Everything at once, from many origins, under -race: the single mutex is the
// design, and this is what would catch a path that forgot it.
func TestConcurrentUse(t *testing.T) {
	r, _ := newRegistry(t, 4, time.Minute)
	tok, _ := r.IssueLink("peer-2", 0)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			origin := fmt.Sprintf("198.51.100.%d", n)
			c, err := r.Allocate(rendezvous.PeerID(origin), time.Minute)
			if err != nil {
				t.Errorf("Allocate: %v", err)
				return
			}
			minted, err := r.MintFromLink(tok, origin, time.Minute)
			if err != nil {
				t.Errorf("MintFromLink: %v", err)
				return
			}
			for _, code := range []rendezvous.Code{c, minted} {
				if _, err := r.Redeem(string(code), origin); err != nil {
					t.Errorf("Redeem: %v", err)
				}
			}
			_, _ = r.Redeem("000000", origin)
			r.Sweep()
			_ = r.Stats()
		}(i)
	}
	wg.Wait()
}
