// Package plancache is the one caching policy behind every plan-limit
// source: fetch in the background at most once per TTL, back off after a
// rate limit, and turn a login problem into "--" with a single warning.
package plancache

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// TTL is how long a fetched value is served before the next fetch.
	TTL = 60 * time.Second

	// RateLimitBackoff replaces TTL after the API answered 429: the usage
	// endpoints rate-limit aggressively.
	RateLimitBackoff = 5 * time.Minute
)

var (
	// ErrRateLimited marks a 429; the next attempt waits RateLimitBackoff.
	ErrRateLimited = errors.New("rate limited")

	// ErrLogin marks a credential problem only the user can fix (an expired
	// or missing token). The cached value is dropped, because it would go
	// stale unnoticed, and the warning is logged once until a fetch succeeds.
	ErrLogin = errors.New("login required")
)

// Fetcher obtains the current limits of one assistant. It runs in a
// background goroutine, one call at a time, and applies its own timeouts.
type Fetcher interface {
	Fetch(ctx context.Context, now time.Time) (domain.PlanUsage, error)
}

// Cache implements domain.PlanSource over a Fetcher.
type Cache struct {
	name    string
	fetcher Fetcher
	logger  *slog.Logger

	mu         sync.Mutex
	cached     domain.PlanUsage
	nextFetch  time.Time
	hasValue   bool
	inflight   bool
	lastWarned string

	// fetchMu serialises Fetch calls: a direct Refresh may overlap a
	// Plan-spawned one, and fetchers keep state of their own (tokens, the
	// forecast history) that is not safe for two goroutines.
	fetchMu sync.Mutex
}

var _ domain.PlanSource = (*Cache)(nil)

// New wraps a fetcher; name labels the log lines ("claude", "agy", "codex").
func New(name string, fetcher Fetcher, logger *slog.Logger) *Cache {
	return &Cache{name: name, fetcher: fetcher, logger: logger}
}

// Plan returns the cached limits and, when they are due, refreshes them in
// the background: a fetch can take seconds and the caller sends a frame
// every two seconds.
func (c *Cache) Plan(now time.Time) domain.PlanUsage {
	c.mu.Lock()
	defer c.mu.Unlock()

	due := !c.hasValue || !now.Before(c.nextFetch)
	if due && !c.inflight {
		c.inflight = true

		go c.Refresh(now)
	}

	if !c.hasValue {
		return domain.UnknownPlanUsage()
	}

	return c.cached
}

// Refresh fetches synchronously and stores the outcome; Plan calls it in the
// background, tests call it directly. The network work runs without the
// lock so Plan stays instant meanwhile.
func (c *Cache) Refresh(now time.Time) {
	c.fetchMu.Lock()
	plan, err := c.fetcher.Fetch(context.Background(), now)
	c.fetchMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.inflight = false
	c.nextFetch = now.Add(TTL)

	if err != nil {
		c.warn(err)

		if errors.Is(err, ErrRateLimited) {
			c.nextFetch = now.Add(RateLimitBackoff)
		}

		if !c.hasValue || errors.Is(err, ErrLogin) {
			c.cached = domain.UnknownPlanUsage()
			c.hasValue = true
		}

		return
	}

	// One line when a real value arrives after none: the service runs
	// without debug logging, and this is the only sign the credentials work.
	if !c.hasValue || !hasKnownLimit(c.cached) {
		c.logger.Info("plan limits available", "provider", c.name,
			"five_hour_pct", plan.FiveHour.Pct, "weekly_pct", plan.Weekly.Pct)
	}

	c.cached = plan
	c.hasValue = true
	c.lastWarned = ""
}

// warn logs a fetch failure; a login problem is repeated only once until it
// clears, a stale token would otherwise log every minute.
func (c *Cache) warn(err error) {
	if errors.Is(err, ErrLogin) {
		if c.lastWarned == err.Error() {
			return
		}

		c.lastWarned = err.Error()
	}

	c.logger.Warn("fetch plan limits", "provider", c.name, "error", err)
}

func hasKnownLimit(p domain.PlanUsage) bool {
	return p.FiveHour.Pct != domain.UnknownPct || p.Weekly.Pct != domain.UnknownPct
}
