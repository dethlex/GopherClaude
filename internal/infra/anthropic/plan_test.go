package anthropic

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const sampleResponse = `{
  "five_hour": {"utilization": 36.0, "resets_at": "2026-06-11T21:09:59.949097+00:00"},
  "seven_day": {"utilization": 17.4, "resets_at": "2026-06-13T21:59:59.949118+00:00"},
  "seven_day_sonnet": {"utilization": 0.0, "resets_at": "2026-06-13T21:59:59.949127+00:00"},
  "extra_usage": {
    "is_enabled": true,
    "monthly_limit": 5000,
    "used_credits": 3266.0,
    "utilization": 65.32,
    "currency": "EUR"
  }
}`

func testFetcher(t *testing.T, handler http.HandlerFunc) *PlanFetcher {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	f := NewPlanFetcher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.token = func(context.Context) (string, error) { return "test-token", nil }
	f.client = server.Client()
	f.url = server.URL

	return f
}

func TestPlanParsesResponse(t *testing.T) {
	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}

		_, _ = w.Write([]byte(sampleResponse))
	})

	got := f.Plan(time.Now())

	if got.FiveHour.Pct != 36 {
		t.Errorf("FiveHour.Pct = %d, want 36", got.FiveHour.Pct)
	}

	if got.Weekly.Pct != 17 {
		t.Errorf("Weekly.Pct = %d, want 17", got.Weekly.Pct)
	}

	wantReset := time.Date(2026, 6, 11, 21, 9, 59, 949097000, time.UTC)
	if !got.FiveHour.ResetsAt.Equal(wantReset) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v", got.FiveHour.ResetsAt, wantReset)
	}

	if got.CreditsPct != 65 {
		t.Errorf("CreditsPct = %d, want 65", got.CreditsPct)
	}

	if got.CreditsText != "32.66/50" {
		t.Errorf("CreditsText = %q, want %q", got.CreditsText, "32.66/50")
	}
}

func TestPlanCachesWithinTTL(t *testing.T) {
	calls := 0

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		calls++

		_, _ = w.Write([]byte(sampleResponse))
	})

	now := time.Now()

	f.Plan(now)
	f.Plan(now.Add(10 * time.Second))
	f.Plan(now.Add(30 * time.Second))

	if calls != 1 {
		t.Errorf("api calls = %d, want 1 (cached within TTL)", calls)
	}

	f.Plan(now.Add(cacheTTL + time.Second))

	if calls != 2 {
		t.Errorf("api calls = %d, want 2 (refreshed after TTL)", calls)
	}
}

func TestPlanDegradesOnFailure(t *testing.T) {
	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})

	got := f.Plan(time.Now())

	if got.FiveHour.Pct != domain.UnknownPct || got.Weekly.Pct != domain.UnknownPct {
		t.Errorf("Plan() = %+v, want unknown values", got)
	}
}

func TestPlanKeepsLastValueOnFailure(t *testing.T) {
	fail := false

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "nope", http.StatusInternalServerError)

			return
		}

		_, _ = w.Write([]byte(sampleResponse))
	})

	now := time.Now()

	first := f.Plan(now)
	if first.FiveHour.Pct != 36 {
		t.Fatalf("first Plan() = %+v", first)
	}

	fail = true

	second := f.Plan(now.Add(cacheTTL + time.Second))
	if second.FiveHour.Pct != 36 {
		t.Errorf("after failure Plan() = %+v, want last known value", second)
	}
}

func TestPlanForecastsFiveHourETA(t *testing.T) {
	pct := 40.0

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		body := `{"five_hour":{"utilization":` + strconv.FormatFloat(pct, 'f', 1, 64) +
			`,"resets_at":"2026-06-11T23:59:59+00:00"}}`
		_, _ = w.Write([]byte(body))
	})

	start := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)

	// 40% -> 42% -> 44% over 4 minutes: 1 pct/min, 56% left => 56m ETA,
	// well before the ~6h reset.
	if got := f.Plan(start); got.FiveHourETA != 0 {
		t.Errorf("first sample ETA = %v, want 0 (not enough history)", got.FiveHourETA)
	}

	pct = 42.0

	f.Plan(start.Add(2 * time.Minute))

	pct = 44.0

	got := f.Plan(start.Add(4 * time.Minute))

	want := 56 * time.Minute
	if got.FiveHourETA != want {
		t.Errorf("ETA = %v, want %v", got.FiveHourETA, want)
	}
}

func TestPlanForecastSilentWhenResetComesFirst(t *testing.T) {
	pct := 10.0

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		body := `{"five_hour":{"utilization":` + strconv.FormatFloat(pct, 'f', 1, 64) +
			`,"resets_at":"2026-06-11T19:00:00+00:00"}}`
		_, _ = w.Write([]byte(body))
	})

	start := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)

	f.Plan(start)

	pct = 11.0 // 0.5 pct/min: would take ~178m, but the reset is in 56m

	got := f.Plan(start.Add(2 * time.Minute))

	if got.FiveHourETA != 0 {
		t.Errorf("ETA = %v, want 0 when the reset arrives first", got.FiveHourETA)
	}
}

func TestPlanForecastResetsHistoryOnDrop(t *testing.T) {
	pct := 80.0

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		body := `{"five_hour":{"utilization":` + strconv.FormatFloat(pct, 'f', 1, 64) +
			`,"resets_at":"2026-06-12T06:00:00+00:00"}}`
		_, _ = w.Write([]byte(body))
	})

	start := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)

	f.Plan(start)

	pct = 85.0

	f.Plan(start.Add(2 * time.Minute))

	pct = 3.0 // window reset

	got := f.Plan(start.Add(4 * time.Minute))

	if got.FiveHourETA != 0 {
		t.Errorf("ETA = %v, want 0 right after a window reset", got.FiveHourETA)
	}
}

func TestPlanBacksOffOnRateLimit(t *testing.T) {
	calls := 0

	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		calls++

		http.Error(w, "slow down", http.StatusTooManyRequests)
	})

	now := time.Now()

	f.Plan(now)
	f.Plan(now.Add(cacheTTL + time.Second)) // normal TTL: still backing off

	if calls != 1 {
		t.Errorf("api calls = %d, want 1 (rate-limit backoff)", calls)
	}

	f.Plan(now.Add(rateLimitBackoff + time.Second))

	if calls != 2 {
		t.Errorf("api calls = %d, want 2 after the backoff expires", calls)
	}
}

func TestFormatCredits(t *testing.T) {
	tests := []struct {
		cents float64
		want  string
	}{
		{3266, "32.66"},
		{5000, "50"},
		{50, "0.5"},
		{0, "0"},
	}

	for _, tt := range tests {
		if got := formatCredits(tt.cents); got != tt.want {
			t.Errorf("formatCredits(%v) = %q, want %q", tt.cents, got, tt.want)
		}
	}
}
