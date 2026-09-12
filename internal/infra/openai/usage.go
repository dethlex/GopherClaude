// Package openai fetches Codex plan limits from the ChatGPT backend — the
// same numbers Codex shows in /status — with the credentials Codex keeps in
// $CODEX_HOME/auth.json.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	usageURL = "https://chatgpt.com/backend-api/wham/usage"

	// The backend keys client behaviour off these; mirror the CLI.
	userAgent  = "codex_cli_rs/0.150.1"
	originator = "codex_cli_rs"

	requestTimeout   = 8 * time.Second
	cacheTTL         = 60 * time.Second
	rateLimitBackoff = 5 * time.Minute
	maxBodyBytes     = 1 << 20
	percentMax       = 100

	// Windows are told apart by length only: Plus/Pro report a 5-hour
	// primary and a weekly secondary window, Team a single weekly one.
	fiveHourMax = 6 * time.Hour
	weeklyMin   = 6 * 24 * time.Hour

	jwtParts = 3
)

var (
	errRateLimited = errors.New("rate limited")
	errNoAuthFile  = errors.New("no auth.json: run codex to log in")
	errBadAuthFile = errors.New("bad auth.json: run codex to log in")
	errNoToken     = errors.New("auth.json has no ChatGPT tokens (logged in with an API key?)")
	errExpired     = errors.New("access token expired; run codex once to refresh the login")
	errRejected    = errors.New("access token rejected (401); run codex once to refresh the login")
)

// authFile is the part of Codex's auth.json the fetcher reads.
type authFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// UsageFetcher implements domain.PlanSource for Codex.
//
// It only ever reads auth.json: the access token is a ten-day JWT that Codex
// renews itself while running, and OpenAI rotates refresh tokens, so a
// refresh from here would invalidate the one Codex holds. An expired or
// rejected token therefore means "--" on the badge until Codex is run again.
type UsageFetcher struct {
	authPath string
	client   *http.Client
	logger   *slog.Logger
	usageURL string

	mu        sync.Mutex
	cached    domain.PlanUsage
	nextFetch time.Time
	hasValue  bool
	inflight  bool

	// lastWarned de-duplicates the login warnings: a stale token would
	// otherwise log every minute.
	lastWarned string
}

var _ domain.PlanSource = (*UsageFetcher)(nil)

func NewUsageFetcher(authPath string, logger *slog.Logger) *UsageFetcher {
	return &UsageFetcher{
		authPath: authPath,
		client:   &http.Client{Timeout: requestTimeout},
		logger:   logger.With("module", "codex-quota"),
		usageURL: usageURL,
	}
}

// Plan returns the cached limits and, when they are due, refreshes them in
// the background: the caller sends a frame every two seconds and must not
// wait on the network.
func (f *UsageFetcher) Plan(now time.Time) domain.PlanUsage {
	f.mu.Lock()
	defer f.mu.Unlock()

	due := !f.hasValue || !now.Before(f.nextFetch)
	if due && !f.inflight {
		f.inflight = true

		go f.update(now)
	}

	if !f.hasValue {
		return domain.UnknownPlanUsage()
	}

	return f.cached
}

// update fetches synchronously and stores the outcome. The network work runs
// without the lock so Plan stays instant meanwhile.
func (f *UsageFetcher) update(now time.Time) {
	plan, err := f.fetch(now)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.inflight = false
	f.nextFetch = now.Add(cacheTTL)

	if err != nil {
		f.warn(err)

		if errors.Is(err, errRateLimited) {
			f.nextFetch = now.Add(rateLimitBackoff)
		}

		// A login problem is not a transient failure: the last value would
		// go stale unnoticed, so the bars fall back to "--".
		if !f.hasValue || isLoginError(err) {
			f.cached = domain.UnknownPlanUsage()
			f.hasValue = true
		}

		return
	}

	// One line on the first real value: the service runs without debug
	// logging, and this is the only sign the token works.
	if !f.hasValue || f.cached.Weekly.Pct == domain.UnknownPct {
		f.logger.Info("codex quota available", "five_hour_pct", plan.FiveHour.Pct, "weekly_pct", plan.Weekly.Pct)
	}

	f.cached = plan
	f.hasValue = true
	f.lastWarned = ""
}

// warn logs a fetch failure, repeating a login problem only once until it
// clears.
func (f *UsageFetcher) warn(err error) {
	if isLoginError(err) {
		if f.lastWarned == err.Error() {
			return
		}

		f.lastWarned = err.Error()
	}

	f.logger.Warn("fetch codex quota", "error", err)
}

func isLoginError(err error) bool {
	return errors.Is(err, errNoAuthFile) || errors.Is(err, errBadAuthFile) || errors.Is(err, errNoToken) || errors.Is(err, errExpired) || errors.Is(err, errRejected)
}

func (f *UsageFetcher) fetch(now time.Time) (domain.PlanUsage, error) {
	access, account, err := f.credentials(now)
	if err != nil {
		return domain.PlanUsage{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.usageURL, nil)
	if err != nil {
		return domain.PlanUsage{}, err
	}

	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("ChatGPT-Account-Id", account)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("originator", originator)

	resp, err := f.client.Do(req)
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("usage request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("read usage: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return parseUsage(body)
	case http.StatusTooManyRequests:
		return domain.PlanUsage{}, fmt.Errorf("usage: %s: %w", resp.Status, errRateLimited)
	case http.StatusUnauthorized:
		return domain.PlanUsage{}, errRejected
	default:
		return domain.PlanUsage{}, fmt.Errorf("usage: %s", resp.Status)
	}
}

// credentials reads auth.json on every fetch (Codex rewrites it when it
// refreshes) and refuses a token whose JWT expiry has passed: the backend
// would only answer 401.
func (f *UsageFetcher) credentials(now time.Time) (access, account string, err error) {
	raw, err := os.ReadFile(f.authPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: %w", errNoAuthFile, err)
		}
		return "", "", fmt.Errorf("read auth file: %w", err)
	}

	var af authFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return "", "", fmt.Errorf("%w: %w", errBadAuthFile, err)
	}

	if af.Tokens.AccessToken == "" || af.Tokens.AccountID == "" {
		return "", "", errNoToken
	}

	if exp := jwtExpiry(af.Tokens.AccessToken); !exp.IsZero() && !now.Before(exp) {
		return "", "", errExpired
	}

	return af.Tokens.AccessToken, af.Tokens.AccountID, nil
}

// jwtExpiry reads the exp claim of an unverified JWT; zero when the token is
// not a JWT or carries no expiry.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != jwtParts {
		return time.Time{}
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}

	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}

	return time.Unix(claims.Exp, 0)
}

// usageResponse is the part of wham/usage the badge reads.
type usageResponse struct {
	RateLimit struct {
		Primary   *usageWindow `json:"primary_window"`
		Secondary *usageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type usageWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt       int64   `json:"reset_at"`
}

// parseUsage maps the windows onto the badge's two bars by their length; a
// plan without a 5-hour window (Team) leaves that bar unknown.
func parseUsage(body []byte) (domain.PlanUsage, error) {
	var ur usageResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return domain.PlanUsage{}, fmt.Errorf("decode usage: %w", err)
	}

	plan := domain.UnknownPlanUsage()
	found := false

	for _, w := range []*usageWindow{ur.RateLimit.Primary, ur.RateLimit.Secondary} {
		if w == nil || w.WindowSeconds <= 0 {
			continue
		}

		limit := domain.Limit{Pct: clampPct(w.UsedPercent)}
		if w.ResetAt > 0 {
			limit.ResetsAt = time.Unix(w.ResetAt, 0)
		}

		switch window := time.Duration(w.WindowSeconds) * time.Second; {
		case window <= fiveHourMax:
			plan.FiveHour, found = limit, true
		case window >= weeklyMin:
			plan.Weekly, found = limit, true
		}
	}

	if !found {
		return plan, errors.New("usage response has no rate-limit windows")
	}

	return plan, nil
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
