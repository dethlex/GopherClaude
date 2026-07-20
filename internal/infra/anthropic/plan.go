// Package anthropic fetches plan limit utilization from the same OAuth API
// the Claude Desktop "Plan usage" panel uses.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	usageURL  = "https://api.anthropic.com/api/oauth/usage"
	oauthBeta = "oauth-2025-04-20"

	// Claude Code stores its OAuth credentials in the login keychain.
	keychainService = "Claude Code-credentials"

	fetchTimeout = 5 * time.Second
	cacheTTL     = 60 * time.Second

	// The usage endpoint rate-limits aggressively; after a 429 stay away
	// noticeably longer than the regular polling interval.
	rateLimitBackoff = 5 * time.Minute

	centsPerUnit  = 100
	percentMax    = 100
	maxBodyBytes  = 1 << 20
	creditDecimal = 2

	// Burn-rate forecast tuning: predict only from a window of recent
	// samples spanning at least minForecastSpan, and ignore growth slower
	// than the noise floor.
	historyWindow    = 15 * time.Minute
	minForecastSpan  = 2 * time.Minute
	minRatePctPerMin = 0.05
	resetDropPct     = 1.0
)

// TokenFunc returns a bearer token for the usage API.
type TokenFunc func(ctx context.Context) (string, error)

// PlanFetcher polls the usage API lazily: at most once per cacheTTL, from
// within Plan(). Failures keep the previous value; before the first success
// every field is UnknownPct.
type PlanFetcher struct {
	client *http.Client
	token  TokenFunc
	url    string
	logger *slog.Logger

	mu        sync.Mutex
	cached    domain.PlanUsage
	nextFetch time.Time
	hasValue  bool
	history   []sample
}

var errRateLimited = errors.New("rate limited")

// sample is one observed 5-hour utilization point for the burn-rate forecast.
type sample struct {
	at  time.Time
	pct float64
}

func NewPlanFetcher(logger *slog.Logger) *PlanFetcher {
	return &PlanFetcher{
		client: &http.Client{Timeout: fetchTimeout},
		token:  keychainToken,
		url:    usageURL,
		logger: logger.With("module", "plan"),
	}
}

func (f *PlanFetcher) Plan(now time.Time) domain.PlanUsage {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.hasValue && now.Before(f.nextFetch) {
		return f.cached
	}

	plan, err := f.fetch()
	if err != nil {
		f.logger.Warn("fetch plan usage", "error", err)

		// Back off after a failure too, but keep serving the last
		// known value.
		f.nextFetch = now.Add(cacheTTL)
		if errors.Is(err, errRateLimited) {
			f.nextFetch = now.Add(rateLimitBackoff)
		}

		if !f.hasValue {
			f.cached = domain.UnknownPlanUsage()
			f.hasValue = true
		}

		return f.cached
	}

	plan.FiveHourETA = f.forecast(plan, now)

	f.cached = plan
	f.nextFetch = now.Add(cacheTTL)
	f.hasValue = true

	return f.cached
}

// forecast tracks 5-hour utilization samples and extrapolates when the limit
// will be exhausted at the current pace. It returns zero unless the forecast
// is meaningful AND the limit runs out before its scheduled reset.
func (f *PlanFetcher) forecast(plan domain.PlanUsage, now time.Time) time.Duration {
	if plan.FiveHour.Pct == domain.UnknownPct {
		return 0
	}

	pct := float64(plan.FiveHour.Pct)

	// A drop means the 5-hour window reset; old samples describe the
	// previous window and must not feed the forecast.
	if n := len(f.history); n > 0 && pct < f.history[n-1].pct-resetDropPct {
		f.history = f.history[:0]
	}

	f.history = append(f.history, sample{at: now, pct: pct})

	cutoff := now.Add(-historyWindow)
	for len(f.history) > 1 && f.history[0].at.Before(cutoff) {
		f.history = f.history[1:]
	}

	first, last := f.history[0], f.history[len(f.history)-1]

	span := last.at.Sub(first.at)
	if span < minForecastSpan {
		return 0
	}

	ratePerMin := (last.pct - first.pct) / span.Minutes()
	if ratePerMin < minRatePctPerMin {
		return 0
	}

	eta := time.Duration((percentMax - last.pct) / ratePerMin * float64(time.Minute))

	if !plan.FiveHour.ResetsAt.IsZero() && now.Add(eta).After(plan.FiveHour.ResetsAt) {
		return 0 // the reset arrives first; nothing to warn about
	}

	return eta
}

type (
	bucket struct {
		Utilization float64   `json:"utilization"`
		ResetsAt    time.Time `json:"resets_at"`
	}

	extraUsage struct {
		IsEnabled    bool    `json:"is_enabled"`
		MonthlyLimit float64 `json:"monthly_limit"`
		UsedCredits  float64 `json:"used_credits"`
		Utilization  float64 `json:"utilization"`
	}

	usageResponse struct {
		FiveHour *bucket     `json:"five_hour"`
		SevenDay *bucket     `json:"seven_day"`
		Extra    *extraUsage `json:"extra_usage"`
	}
)

func (f *PlanFetcher) fetch() (domain.PlanUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	token, err := f.token(ctx)
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("obtain oauth token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", oauthBeta)

	resp, err := f.client.Do(req)
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("call usage api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return domain.PlanUsage{}, fmt.Errorf("usage api status %s: %w", resp.Status, errRateLimited)
	}

	if resp.StatusCode != http.StatusOK {
		return domain.PlanUsage{}, fmt.Errorf("usage api status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("read body: %w", err)
	}

	var parsed usageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return domain.PlanUsage{}, fmt.Errorf("decode body: %w", err)
	}

	return toPlanUsage(parsed), nil
}

func toPlanUsage(r usageResponse) domain.PlanUsage {
	plan := domain.UnknownPlanUsage()

	if r.FiveHour != nil {
		plan.FiveHour = toLimit(*r.FiveHour)
	}

	if r.SevenDay != nil {
		plan.Weekly = toLimit(*r.SevenDay)
	}

	if r.Extra != nil && r.Extra.IsEnabled && r.Extra.MonthlyLimit > 0 {
		plan.CreditsPct = clampPct(r.Extra.Utilization)
		plan.CreditsText = formatCredits(r.Extra.UsedCredits) + "/" + formatCredits(r.Extra.MonthlyLimit)
	}

	return plan
}

func toLimit(b bucket) domain.Limit {
	return domain.Limit{
		Pct:      clampPct(b.Utilization),
		ResetsAt: b.ResetsAt,
	}
}

func clampPct(v float64) int {
	pct := int(math.Round(v))
	if pct < 0 {
		return 0
	}

	if pct > percentMax {
		return percentMax
	}

	return pct
}

// formatCredits renders an amount in cents compactly: 3266 -> "32.66",
// 5000 -> "50".
func formatCredits(cents float64) string {
	units := cents / centsPerUnit

	s := strconv.FormatFloat(units, 'f', creditDecimal, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")

	return s
}

var errTokenExpired = errors.New("oauth token expired; waiting for Claude Code to refresh it")

// expirySkew treats a token as expired slightly early so it is not used right
// as it lapses mid-request.
const expirySkew = 60 * time.Second

// keychainToken reads the Claude Code OAuth access token from the macOS login
// keychain, exactly like the CLI itself does. The `security` binary is used
// (instead of keychain APIs) so the ACL treats us like any other CLI.
//
// When the stored token is already expired it returns errTokenExpired without
// a token: the agent does not own the refresh flow (that would rotate the
// refresh token and could log Claude Code out), so it must wait for Claude
// Code to refresh the credential. Skipping the call also avoids hammering the
// rate-limited endpoint with requests that can only 401.
func keychainToken(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx,
		"security", "find-generic-password", "-s", keychainService, "-w",
	).Output()
	if err != nil {
		return "", fmt.Errorf("read keychain item %q: %w", keychainService, err)
	}

	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}

	if err := json.Unmarshal(out, &creds); err != nil {
		return "", fmt.Errorf("parse keychain credentials: %w", err)
	}

	if creds.ClaudeAiOauth.AccessToken == "" {
		return "", fmt.Errorf("keychain item %q has no access token", keychainService)
	}

	if tokenExpired(creds.ClaudeAiOauth.ExpiresAt, time.Now()) {
		return "", errTokenExpired
	}

	return creds.ClaudeAiOauth.AccessToken, nil
}

// tokenExpired reports whether an expiresAt (unix millis) is at or past now,
// minus a small skew. A missing/zero expiry is treated as not expired.
func tokenExpired(expiresAtMillis int64, now time.Time) bool {
	if expiresAtMillis == 0 {
		return false
	}

	return !now.Before(time.UnixMilli(expiresAtMillis).Add(-expirySkew))
}
