package plancache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

type fakeFetcher struct {
	calls int32
	fn    func(now time.Time) (domain.PlanUsage, error)
}

func (f *fakeFetcher) Fetch(_ context.Context, now time.Time) (domain.PlanUsage, error) {
	atomic.AddInt32(&f.calls, 1)

	return f.fn(now)
}

func known(pct int) domain.PlanUsage {
	p := domain.UnknownPlanUsage()
	p.FiveHour.Pct = pct

	return p
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCacheServesValueWithinTTL(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	ff := &fakeFetcher{fn: func(time.Time) (domain.PlanUsage, error) { return known(36), nil }}
	c := New("test", ff, discard())

	c.Refresh(now)

	if got := c.Plan(now.Add(10 * time.Second)); got.FiveHour.Pct != 36 {
		t.Fatalf("Plan = %+v", got)
	}

	if !c.nextFetch.Equal(now.Add(TTL)) || atomic.LoadInt32(&ff.calls) != 1 {
		t.Errorf("nextFetch=%v calls=%d, want now+TTL and 1", c.nextFetch, ff.calls)
	}
}

func TestCachePlanNeverBlocksAndPicksUpTheFetch(t *testing.T) {
	release := make(chan struct{})
	ff := &fakeFetcher{fn: func(time.Time) (domain.PlanUsage, error) {
		<-release

		return known(42), nil
	}}
	c := New("test", ff, discard())

	start := time.Now()

	if got := c.Plan(start); got.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan before the first fetch = %+v, want unknown", got)
	}

	if time.Since(start) > time.Second {
		t.Errorf("Plan blocked for %v; the fetch must run in the background", time.Since(start))
	}

	// A second call while the fetch is in flight must not start another.
	c.Plan(start)

	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&ff.calls) != 1 {
		t.Errorf("calls = %d, want 1 while in flight", ff.calls)
	}

	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Plan(start).FiveHour.Pct == 42 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Error("the background fetch never landed in Plan")
}

func TestCacheRateLimitBacksOff(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	ff := &fakeFetcher{fn: func(time.Time) (domain.PlanUsage, error) {
		return domain.PlanUsage{}, fmt.Errorf("429: %w", ErrRateLimited)
	}}
	c := New("test", ff, discard())

	c.Refresh(now)

	if !c.nextFetch.Equal(now.Add(RateLimitBackoff)) {
		t.Errorf("nextFetch = %v, want the rate-limit backoff", c.nextFetch)
	}

	if got := c.Plan(now); got.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan = %+v, want unknown before any value", got)
	}
}

func TestCacheTransientErrorKeepsLastValue(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	fail := false
	ff := &fakeFetcher{fn: func(time.Time) (domain.PlanUsage, error) {
		if fail {
			return domain.PlanUsage{}, errors.New("boom")
		}

		return known(36), nil
	}}
	c := New("test", ff, discard())

	c.Refresh(now)

	fail = true
	later := now.Add(TTL + time.Second)
	c.Refresh(later)

	if got := c.Plan(later); got.FiveHour.Pct != 36 {
		t.Errorf("Plan after a transient failure = %+v, want the last value", got)
	}

	if !c.nextFetch.Equal(later.Add(TTL)) {
		t.Errorf("nextFetch = %v, want the regular TTL after a transient failure", c.nextFetch)
	}
}

func TestCacheLoginErrorDropsValueAndWarnsOnce(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	var logs bytes.Buffer

	loginBroken := false
	ff := &fakeFetcher{fn: func(time.Time) (domain.PlanUsage, error) {
		if loginBroken {
			return domain.PlanUsage{}, fmt.Errorf("%w: token expired", ErrLogin)
		}

		return known(36), nil
	}}
	c := New("test", ff, slog.New(slog.NewTextHandler(&logs, nil)))

	c.Refresh(now)

	loginBroken = true
	for i := 1; i <= 3; i++ {
		c.Refresh(now.Add(time.Duration(i) * TTL))
	}

	if got := c.Plan(now.Add(3 * TTL)); got.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan after a login error = %+v, want unknown rather than the stale 36", got)
	}

	if n := strings.Count(logs.String(), "fetch plan limits"); n != 1 {
		t.Errorf("warnings = %d, want 1:\n%s", n, logs.String())
	}

	// A success clears the de-duplication and announces the value again;
	// the next login problem is reported once more.
	loginBroken = false
	c.Refresh(now.Add(4 * TTL))
	loginBroken = true
	c.Refresh(now.Add(5 * TTL))

	if n := strings.Count(logs.String(), "fetch plan limits"); n != 2 {
		t.Errorf("warnings after recovery = %d, want 2:\n%s", n, logs.String())
	}

	if n := strings.Count(logs.String(), "plan limits available"); n != 2 {
		t.Errorf("availability lines = %d, want 2 (first value, and again after the drop):\n%s", n, logs.String())
	}
}
