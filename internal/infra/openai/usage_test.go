package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/plancache"
)

// Captured from a real wham/usage response (Team plan: one weekly window).
const teamFixture = `{"plan_type":"team","rate_limit":{"allowed":true,"limit_reached":false,` +
	`"primary_window":{"used_percent":17,"limit_window_seconds":604800,"reset_after_seconds":519000,"reset_at":1789806830},` +
	`"secondary_window":null},"credits":{"has_credits":false,"unlimited":false,"balance":null}}`

// Plus/Pro shape: 5-hour primary plus weekly secondary.
const plusFixture = `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,` +
	`"primary_window":{"used_percent":44.4,"limit_window_seconds":18000,"reset_after_seconds":11000,"reset_at":1789300000},` +
	`"secondary_window":{"used_percent":90.2,"limit_window_seconds":604800,"reset_after_seconds":300000,"reset_at":1789600000}}}`

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseUsageTeam(t *testing.T) {
	plan, err := parseUsage([]byte(teamFixture))
	if err != nil {
		t.Fatal(err)
	}

	if plan.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("FiveHour.Pct = %d, want unknown (Team has no 5-hour window)", plan.FiveHour.Pct)
	}

	if plan.Weekly.Pct != 17 || !plan.Weekly.ResetsAt.Equal(time.Unix(1789806830, 0)) {
		t.Errorf("Weekly = %+v", plan.Weekly)
	}

	if plan.CreditsPct != domain.UnknownPct {
		t.Errorf("CreditsPct = %d, want unknown", plan.CreditsPct)
	}
}

func TestParseUsagePlus(t *testing.T) {
	plan, err := parseUsage([]byte(plusFixture))
	if err != nil {
		t.Fatal(err)
	}

	if plan.FiveHour.Pct != 44 || plan.Weekly.Pct != 90 {
		t.Errorf("plan = 5h %d / wk %d, want 44 / 90", plan.FiveHour.Pct, plan.Weekly.Pct)
	}
}

func TestParseUsageNoWindows(t *testing.T) {
	if _, err := parseUsage([]byte(`{"plan_type":"free","rate_limit":{"primary_window":null,"secondary_window":null}}`)); err == nil {
		t.Error("expected an error when no window is present")
	}
}

// jwt builds an unsigned token with the given expiry, the way the fetcher
// reads it (header and signature are irrelevant).
func jwt(exp time.Time) string {
	claims, _ := json.Marshal(map[string]int64{"exp": exp.Unix()})
	seg := base64.RawURLEncoding.EncodeToString

	return seg([]byte(`{"alg":"RS256"}`)) + "." + seg(claims) + "." + seg([]byte("sig"))
}

func authJSON(access string) string {
	return `{"OPENAI_API_KEY":null,"last_refresh":"2026-09-07T13:46:15Z","tokens":{"access_token":"` + access +
		`","account_id":"acct-1","id_token":"x","refresh_token":"rt"}}`
}

func testFetcher(t *testing.T, auth string, logger *slog.Logger, handler http.HandlerFunc) (*UsageFetcher, string) {
	t.Helper()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := NewUsageFetcher(authPath, logger)
	f.client = srv.Client()
	f.usageURL = srv.URL + "/wham/usage"

	return f, authPath
}

func TestUsageFetcherFetchesAndCaches(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	token := jwt(now.Add(5 * 24 * time.Hour))

	var calls int32

	f, authPath := testFetcher(t, authJSON(token), discardLogger(), func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("ChatGPT-Account-Id") != "acct-1" ||
			r.Header.Get("User-Agent") != userAgent || r.Header.Get("originator") != originator {
			http.Error(w, "missing headers", http.StatusBadRequest)

			return
		}

		_, _ = w.Write([]byte(teamFixture))
	})

	before, _ := os.ReadFile(authPath)

	f.Refresh(now)

	if plan := f.Plan(now); plan.Weekly.Pct != 17 {
		t.Fatalf("Plan = %+v", plan)
	}

	f.Plan(now.Add(10 * time.Second))

	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("usage calls = %d, want 1 (cached within TTL)", calls)
	}

	after, _ := os.ReadFile(authPath)
	if string(before) != string(after) {
		t.Error("auth.json was modified; the fetcher must only read it")
	}
}

func TestUsageFetcherPicksUpRewrittenAuthFile(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	first := jwt(now.Add(24 * time.Hour))
	second := jwt(now.Add(10 * 24 * time.Hour))

	f, authPath := testFetcher(t, authJSON(first), discardLogger(), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+second {
			_, _ = w.Write([]byte(plusFixture))

			return
		}

		_, _ = w.Write([]byte(teamFixture))
	})

	f.Refresh(now)

	if plan := f.Plan(now); plan.FiveHour.Pct != domain.UnknownPct {
		t.Fatalf("Plan with the first token = %+v, want the team fixture", plan)
	}

	// Codex refreshed its login: the next fetch must use the new token.
	if err := os.WriteFile(authPath, []byte(authJSON(second)), 0o600); err != nil {
		t.Fatal(err)
	}

	later := now.Add(plancache.TTL + time.Second)
	f.Refresh(later)

	if plan := f.Plan(later); plan.FiveHour.Pct != 44 {
		t.Errorf("Plan with the rewritten file = %+v, want the plus fixture", plan)
	}
}

func TestFetchClassifiesErrors(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		auth      string
		status    int
		want      error
		wantCalls int32
	}{
		{"api key login", `{"OPENAI_API_KEY":"sk-x","last_refresh":null}`, http.StatusOK, plancache.ErrLogin, 0},
		{"expired token", authJSON(jwt(now.Add(-time.Hour))), http.StatusOK, plancache.ErrLogin, 0},
		{"rejected token", authJSON(jwt(now.Add(time.Hour))), http.StatusUnauthorized, plancache.ErrLogin, 1},
		{"rate limited", authJSON(jwt(now.Add(time.Hour))), http.StatusTooManyRequests, plancache.ErrRateLimited, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32

			f, _ := testFetcher(t, tt.auth, discardLogger(), func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				http.Error(w, "no", tt.status)
			})

			if _, err := f.Fetch(context.Background(), now); !errors.Is(err, tt.want) {
				t.Errorf("Fetch error = %v, want %v", err, tt.want)
			}

			if atomic.LoadInt32(&calls) != tt.wantCalls {
				t.Errorf("usage calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}

	// A missing or unreadable auth.json is a login problem too (codex logout
	// removes the file), not a transient failure to retry noisily.
	missing := NewUsageFetcher(filepath.Join(t.TempDir(), "absent.json"), discardLogger())
	if _, err := missing.Fetch(context.Background(), now); !errors.Is(err, plancache.ErrLogin) {
		t.Errorf("Fetch without auth.json = %v, want a login problem", err)
	}

	broken, _ := testFetcher(t, "{not json", discardLogger(), func(http.ResponseWriter, *http.Request) {})
	if _, err := broken.Fetch(context.Background(), now); !errors.Is(err, plancache.ErrLogin) {
		t.Errorf("Fetch with a corrupt auth.json = %v, want a login problem", err)
	}
}

func TestUsageFetcherRejectedTokenDropsStaleValue(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	status := int32(http.StatusOK)

	f, _ := testFetcher(t, authJSON(jwt(now.Add(time.Hour))), discardLogger(), func(w http.ResponseWriter, r *http.Request) {
		if s := atomic.LoadInt32(&status); s != http.StatusOK {
			http.Error(w, "no", int(s))

			return
		}

		_, _ = w.Write([]byte(teamFixture))
	})

	f.Refresh(now)
	atomic.StoreInt32(&status, http.StatusUnauthorized)

	later := now.Add(plancache.TTL + time.Second)
	f.Refresh(later)

	if plan := f.Plan(later); plan.Weekly.Pct != domain.UnknownPct {
		t.Errorf("Plan after 401 = %+v, want unknown rather than the stale 17%%", plan)
	}
}

// Every failure path formats an error; none of them may carry the token or
// the account id into the log.
func TestUsageFetcherNeverLogsSecrets(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	token := jwt(now.Add(time.Hour))

	var (
		status = int32(http.StatusOK)
		body   atomic.Value
		logs   bytes.Buffer
	)

	body.Store(teamFixture)
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	f, _ := testFetcher(t, authJSON(token), logger, func(w http.ResponseWriter, r *http.Request) {
		if s := atomic.LoadInt32(&status); s != http.StatusOK {
			http.Error(w, "no", int(s))

			return
		}

		_, _ = w.Write([]byte(body.Load().(string)))
	})

	f.Refresh(now) // success
	body.Store("not json")
	f.Refresh(now) // malformed body
	atomic.StoreInt32(&status, http.StatusUnauthorized)
	f.Refresh(now) // rejected token
	atomic.StoreInt32(&status, http.StatusTooManyRequests)
	f.Refresh(now) // rate limited

	missing := NewUsageFetcher(filepath.Join(t.TempDir(), "absent.json"), logger)
	missing.Refresh(now)

	out := logs.String()
	if out == "" {
		t.Fatal("expected log output to inspect")
	}

	if strings.Contains(out, token) || strings.Contains(out, "acct-1") {
		t.Errorf("log leaks credentials:\n%s", out)
	}
}

// ~/.codex without auth.json is a normal state (codex logout removes it):
// a login problem to warn about once, not once a minute.
func TestUsageFetcherMissingAuthFileWarnsOnce(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	var logs bytes.Buffer

	f := NewUsageFetcher(filepath.Join(t.TempDir(), "absent.json"), slog.New(slog.NewTextHandler(&logs, nil)))

	f.Refresh(now)
	f.Refresh(now.Add(plancache.TTL + time.Second))
	f.Refresh(now.Add(2*plancache.TTL + 2*time.Second))

	if got := strings.Count(logs.String(), "fetch plan limits"); got != 1 {
		t.Errorf("warnings = %d, want 1:\n%s", got, logs.String())
	}

	if plan := f.Plan(now); plan.Weekly.Pct != domain.UnknownPct {
		t.Errorf("Plan = %+v, want unknown", plan)
	}
}

func TestJWTExpiry(t *testing.T) {
	exp := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	if got := jwtExpiry(jwt(exp)); !got.Equal(exp) {
		t.Errorf("jwtExpiry = %v, want %v", got, exp)
	}

	if got := jwtExpiry("not-a-jwt"); !got.IsZero() {
		t.Errorf("jwtExpiry(opaque) = %v, want zero", got)
	}
}
