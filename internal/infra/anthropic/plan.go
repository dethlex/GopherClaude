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
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/plancache"
)

const (
	usageURL  = "https://api.anthropic.com/api/oauth/usage"
	oauthBeta = "oauth-2025-04-20"

	// Claude Code stores its OAuth credentials in the login keychain; the
	// security CLI exits with this status when the item does not exist.
	keychainService      = "Claude Code-credentials"
	keychainItemNotFound = 44

	fetchTimeout = 5 * time.Second

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

	limitKindModelWeekly = "weekly_scoped"
)

// TokenFunc returns a bearer token for the usage API.
type TokenFunc func(ctx context.Context) (string, error)

// PlanFetcher fetches the usage API; plancache decides when.
type PlanFetcher struct {
	*plancache.Cache

	client  *http.Client
	token   TokenFunc
	url     string
	history []sample
}

// sample is one observed 5-hour utilization point for the burn-rate forecast.
type sample struct {
	at  time.Time
	pct float64
}

func NewPlanFetcher(logger *slog.Logger) *PlanFetcher {
	f := &PlanFetcher{
		client: &http.Client{Timeout: fetchTimeout},
		token:  keychainToken,
		url:    usageURL,
	}
	f.Cache = plancache.New("claude", f, logger.With("module", "plan"))

	return f
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

	// limitEntry is one row of the usage API's self-describing limits list;
	// only the model-scoped weekly row is read here (the session and
	// weekly_all rows duplicate five_hour/seven_day).
	limitEntry struct {
		Kind     string    `json:"kind"`
		Percent  float64   `json:"percent"`
		ResetsAt time.Time `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}

	usageResponse struct {
		FiveHour *bucket      `json:"five_hour"`
		SevenDay *bucket      `json:"seven_day"`
		Extra    *extraUsage  `json:"extra_usage"`
		Limits   []limitEntry `json:"limits"`
	}
)

// Fetch implements plancache.Fetcher: one usage-API call plus the burn-rate
// forecast over the samples this fetcher has seen.
func (f *PlanFetcher) Fetch(ctx context.Context, now time.Time) (domain.PlanUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
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

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return domain.PlanUsage{}, fmt.Errorf("usage api status %s: %w", resp.Status, plancache.ErrRateLimited)
	case http.StatusUnauthorized:
		// The Keychain token expired; Claude Code refreshes it on its next run.
		return domain.PlanUsage{}, fmt.Errorf("usage api status %s: %w", resp.Status, plancache.ErrLogin)
	default:
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

	plan := toPlanUsage(parsed)
	plan.FiveHourETA = f.forecast(plan, now)

	return plan, nil
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

	for _, entry := range r.Limits {
		if entry.Kind == limitKindModelWeekly && entry.Scope != nil && entry.Scope.Model != nil && entry.Scope.Model.DisplayName != "" {
			plan.Model = domain.Limit{
				Pct:      clampPct(entry.Percent),
				ResetsAt: entry.ResetsAt,
			}
			plan.ModelLabel = entry.Scope.Model.DisplayName
			break
		}
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

var errTokenExpired = fmt.Errorf("%w: oauth token expired; waiting for Claude Code to refresh it", plancache.ErrLogin)

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

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == keychainItemNotFound {
		// No item at all: Claude Code was never logged in on this Mac (or
		// logged out). Only the user can fix that, so it is a login problem
		// rather than a failure to retry every minute.
		return "", fmt.Errorf("%w: keychain item %q not found", plancache.ErrLogin, keychainService)
	}

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
		return "", fmt.Errorf("%w: keychain item %q has no access token", plancache.ErrLogin, keychainService)
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
