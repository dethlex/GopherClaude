package google

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Captured from a real retrieveUserQuotaSummary response.
const quotaFixture = `{"groups":[
 {"buckets":[
   {"bucketId":"gemini-weekly","window":"weekly","resetTime":"2026-09-08T12:59:10Z","remainingFraction":0.8232794},
   {"bucketId":"gemini-5h","window":"5h","resetTime":"2026-09-02T13:19:07Z","remainingFraction":0.7777062}],
  "displayName":"Gemini Models"},
 {"buckets":[
   {"bucketId":"3p-weekly","window":"weekly","resetTime":"2026-09-04T14:38:18Z","remainingFraction":0.6206884},
   {"bucketId":"3p-5h","window":"5h","resetTime":"2026-09-02T13:37:47Z","remainingFraction":1}],
  "displayName":"Claude and GPT models"}]}`

func TestParseQuotaPicksGeminiGroup(t *testing.T) {
	plan, err := parseQuota([]byte(quotaFixture))
	if err != nil {
		t.Fatal(err)
	}

	if plan.FiveHour.Pct != 22 { // 1-0.7777 = 22.2%
		t.Errorf("FiveHour.Pct = %d, want 22", plan.FiveHour.Pct)
	}

	if plan.Weekly.Pct != 18 { // 1-0.8233 = 17.7%
		t.Errorf("Weekly.Pct = %d, want 18", plan.Weekly.Pct)
	}

	want := time.Date(2026, 9, 2, 13, 19, 7, 0, time.UTC)
	if !plan.FiveHour.ResetsAt.Equal(want) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v", plan.FiveHour.ResetsAt, want)
	}

	if plan.CreditsPct != domain.UnknownPct {
		t.Errorf("CreditsPct = %d, want unknown", plan.CreditsPct)
	}
}

func TestParseQuotaNoGemini(t *testing.T) {
	if _, err := parseQuota([]byte(`{"groups":[{"buckets":[{"bucketId":"3p-5h","window":"5h"}]}]}`)); err == nil {
		t.Error("expected an error when no gemini buckets are present")
	}
}

func TestScanClientCreds(t *testing.T) {
	blob := "junk\x00" + "123456789012-abcdef0123.apps.googleusercontent.com" + "\x00more" +
		"GOCSPX-" + strings.Repeat("a", 28) + "\x00" +
		"999999-zz.apps.googleusercontent.com" + "GOCSPX-" + strings.Repeat("b", 28) + "tail"

	ids, secrets := scanClientCreds(strings.NewReader(blob))

	if len(ids) != 2 || len(secrets) != 2 {
		t.Fatalf("ids=%v secrets=%d", ids, len(secrets))
	}

	if ids[0] != "123456789012-abcdef0123.apps.googleusercontent.com" {
		t.Errorf("ids[0] = %q", ids[0])
	}
}

func testFetcher(t *testing.T, tokenJSON string, handler http.HandlerFunc) (*QuotaFetcher, *httptest.Server) {
	t.Helper()

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")

	if err := os.WriteFile(tokenPath, []byte(tokenJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := NewQuotaFetcher(tokenPath, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.client = srv.Client()
	f.apiBase = srv.URL + "/v1internal"
	f.tokenEndpoint = srv.URL + "/token"
	f.readCreds = func() ([]string, []string, error) { return []string{"id-1"}, []string{"bad", "good"}, nil }

	return f, srv
}

func TestQuotaFetcherRefreshesExpiredTokenAndCaches(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	expired := `{"token":{"access_token":"stale","refresh_token":"rt","expiry":"` + now.Add(-time.Hour).Format(time.RFC3339) + `"}}`

	var refreshes, quotas int

	f, _ := testFetcher(t, expired, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			refreshes++

			if r.FormValue("client_secret") != "good" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)

				return
			}

			_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			if r.Header.Get("Authorization") != "Bearer fresh" {
				http.Error(w, "no", http.StatusUnauthorized)

				return
			}

			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"aicode-consumers"}`))
		case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			quotas++

			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"aicode-consumers"`) {
				http.Error(w, "wrong project", http.StatusBadRequest)

				return
			}

			_, _ = w.Write([]byte(quotaFixture))
		default:
			http.NotFound(w, r)
		}
	})

	f.update(now)
	plan := f.Plan(now)
	if plan.FiveHour.Pct != 22 {
		t.Fatalf("Plan = %+v", plan)
	}

	// The bad secret is tried first and rejected; the good one succeeds.
	if refreshes != 2 {
		t.Errorf("refresh calls = %d, want 2", refreshes)
	}

	f.Plan(now.Add(10 * time.Second))

	if quotas != 1 {
		t.Errorf("quota calls = %d, want 1 (cached within TTL)", quotas)
	}

	// Within the token lifetime no further refresh happens.
	f.update(now.Add(cacheTTL + time.Second))

	if refreshes != 2 {
		t.Errorf("refresh calls after TTL = %d, want still 2 (token valid for an hour)", refreshes)
	}
}

func TestQuotaFetcherUsesFreshFileToken(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fresh := `{"token":{"access_token":"disk","refresh_token":"rt","expiry":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}}`

	f, _ := testFetcher(t, fresh, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disk" {
			http.Error(w, "unexpected token", http.StatusUnauthorized)

			return
		}

		switch {
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"p"}`))
		default:
			_, _ = w.Write([]byte(quotaFixture))
		}
	})

	f.update(now)

	if plan := f.Plan(now); plan.Weekly.Pct != 18 {
		t.Errorf("Plan = %+v", plan)
	}
}

func TestQuotaFetcherDegradesOnFailure(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fresh := `{"token":{"access_token":"disk","refresh_token":"rt","expiry":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}}`

	f, _ := testFetcher(t, fresh, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})

	f.update(now)
	plan := f.Plan(now)
	if plan.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan = %+v, want unknown", plan)
	}

	if !f.nextFetch.Equal(now.Add(rateLimitBackoff)) {
		t.Errorf("nextFetch = %v, want rate-limit backoff", f.nextFetch)
	}
}

func TestQuotaFetcherPlanNeverBlocks(t *testing.T) {
	f := NewQuotaFetcher(filepath.Join(t.TempDir(), "missing-token"), "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	plan := f.Plan(start)

	if plan.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan before the first fetch = %+v, want unknown", plan)
	}

	if time.Since(start) > time.Second {
		t.Errorf("Plan blocked for %v; the fetch must run in the background", time.Since(start))
	}
}
