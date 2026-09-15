package anthropic

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/plancache"
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
  },
  "limits": [
    {"kind": "session", "group": "session", "percent": 36, "severity": "normal", "resets_at": "2026-06-11T21:09:59.949097+00:00", "scope": null, "is_active": false},
    {"kind": "weekly_all", "group": "weekly", "percent": 17, "severity": "normal", "resets_at": "2026-06-13T21:59:59.949118+00:00", "scope": null, "is_active": true},
    {"kind": "weekly_scoped", "group": "weekly", "percent": 42, "severity": "warning", "resets_at": "2026-06-13T21:59:59.949680+00:00", "scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}, "is_active": false}
  ]
}`

// A response whose scoped weekly limits carry no model: the API describes a
// surface limit and a limit whose scope is null; neither is a model bar.
const noModelResponse = `{
  "five_hour": {"utilization": 3.0, "resets_at": "2026-06-11T21:09:59.949097+00:00"},
  "seven_day": {"utilization": 9.0, "resets_at": "2026-06-13T21:59:59.949118+00:00"},
  "limits": [
    {"kind": "weekly_scoped", "group": "weekly", "percent": 50, "scope": {"model": null, "surface": "cowork"}, "is_active": false},
    {"kind": "weekly_scoped", "group": "weekly", "percent": 60, "scope": null, "is_active": false}
  ]
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

	now := time.Now()
	f.Refresh(now)
	got := f.Plan(now)

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

	// The model-scoped weekly limit comes from the self-describing limits
	// array (the named buckets are codenames now).
	if got.Model.Pct != 42 || got.ModelLabel != "Fable" {
		t.Errorf("Model = %+v / %q, want 42%% labelled Fable", got.Model, got.ModelLabel)
	}

	wantModelReset := time.Date(2026, 6, 13, 21, 59, 59, 949680000, time.UTC)
	if !got.Model.ResetsAt.Equal(wantModelReset) {
		t.Errorf("Model.ResetsAt = %v, want %v", got.Model.ResetsAt, wantModelReset)
	}
}

func TestPlanWithoutModelLimit(t *testing.T) {
	f := testFetcher(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(noModelResponse))
	})

	now := time.Now()
	f.Refresh(now)
	got := f.Plan(now)

	if got.Weekly.Pct != 9 {
		t.Errorf("Weekly.Pct = %d, want 9", got.Weekly.Pct)
	}

	if got.Model.Pct != domain.UnknownPct || got.ModelLabel != "" {
		t.Errorf("Model = %+v / %q, want unknown and unlabelled", got.Model, got.ModelLabel)
	}
}

func TestFetchClassifiesErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"rate limited", http.StatusTooManyRequests, plancache.ErrRateLimited},
		{"unauthorized is a login problem", http.StatusUnauthorized, plancache.ErrLogin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "no", tt.status)
			})

			if _, err := f.Fetch(context.Background(), time.Now()); !errors.Is(err, tt.want) {
				t.Errorf("Fetch error = %v, want %v", err, tt.want)
			}
		})
	}

	// A token the keychain says has expired never reaches the network.
	f := testFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request with an expired token")
	})
	f.token = func(context.Context) (string, error) { return "", errTokenExpired }

	if _, err := f.Fetch(context.Background(), time.Now()); !errors.Is(err, plancache.ErrLogin) {
		t.Errorf("Fetch error = %v, want a login problem", err)
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
	f.Refresh(start)

	if got := f.Plan(start); got.FiveHourETA != 0 {
		t.Errorf("first sample ETA = %v, want 0 (not enough history)", got.FiveHourETA)
	}

	pct = 42.0
	f.Refresh(start.Add(2 * time.Minute))

	pct = 44.0
	at := start.Add(4 * time.Minute)
	f.Refresh(at)

	got := f.Plan(at)

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

	f.Refresh(start)

	pct = 11.0 // 0.5 pct/min: would take ~178m, but the reset is in 56m
	at := start.Add(2 * time.Minute)
	f.Refresh(at)

	if got := f.Plan(at); got.FiveHourETA != 0 {
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

	f.Refresh(start)

	pct = 85.0
	f.Refresh(start.Add(2 * time.Minute))

	pct = 3.0 // window reset
	at := start.Add(4 * time.Minute)
	f.Refresh(at)

	if got := f.Plan(at); got.FiveHourETA != 0 {
		t.Errorf("ETA = %v, want 0 right after a window reset", got.FiveHourETA)
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		millis  int64
		expired bool
	}{
		{"zero means unknown, not expired", 0, false},
		{"well in the future", now.Add(time.Hour).UnixMilli(), false},
		{"already past", now.Add(-time.Hour).UnixMilli(), true},
		{"within skew counts as expired", now.Add(30 * time.Second).UnixMilli(), true},
		{"just beyond skew is still valid", now.Add(2 * time.Minute).UnixMilli(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenExpired(tt.millis, now); got != tt.expired {
				t.Errorf("tokenExpired = %v, want %v", got, tt.expired)
			}
		})
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
