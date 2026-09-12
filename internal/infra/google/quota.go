// Package google fetches Antigravity (Gemini) plan quota from the Code Assist
// API — the same numbers agy shows in its "Models & Quota" panel.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/plancache"
)

const (
	// agy talks to the "daily" Code Assist front end (seen in its logs).
	apiBase       = "https://daily-cloudcode-pa.googleapis.com/v1internal"
	tokenEndpoint = "https://oauth2.googleapis.com/token"

	// The Code Assist backend keys the account's project (and thus the
	// quota) off the client: without agy's User-Agent loadCodeAssist
	// answers 200 with no cloudaicompanionProject at all.
	userAgent = "antigravity-cli/1.1.23"

	// loadCodeAssist must identify the client as Antigravity (its internal
	// name is JETSKI); a generic Gemini client is refused as unsupported.
	clientMetadata = `{"metadata":{"ideType":"JETSKI","platform":"DARWIN_ARM64"}}`

	// A cold fetch scans the agy binary for credentials, refreshes the
	// token and makes two API calls; it runs off the snapshot loop, so the
	// budget can be generous. Individual requests get requestTimeout.
	fetchTimeout   = 20 * time.Second
	requestTimeout = 8 * time.Second
	expirySkew     = 60 * time.Second
	maxBodyBytes   = 1 << 20
	percentMax     = 100

	// Buckets in the quota response are grouped per model family; the
	// Gemini group's bucket ids carry this prefix.
	geminiBucketPrefix = "gemini-"
	windowFiveHour     = "5h"
	windowWeekly       = "weekly"

	// Environment overrides for the OAuth client, in case the agy binary
	// cannot be scanned.
	EnvClientID     = "GOPHERCLAUDE_AGY_CLIENT_ID"
	EnvClientSecret = "GOPHERCLAUDE_AGY_CLIENT_SECRET"
)

var (
	errNoCreds = fmt.Errorf("%w: no oauth client credentials for agy", plancache.ErrLogin)

	clientIDRe     = regexp.MustCompile(`[0-9]{6,}-[a-z0-9]+\.apps\.googleusercontent\.com`)
	clientSecretRe = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{28}`)
)

// tokenFile is agy's on-disk OAuth token (~/.gemini/antigravity-cli/antigravity-oauth-token).
type tokenFile struct {
	Token struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	} `json:"token"`
}

// QuotaFetcher implements domain.PlanSource for Antigravity.
//
// The access token in agy's token file lives one hour and idle agy processes
// do not refresh it, so the fetcher refreshes it itself with the refresh
// token and agy's own OAuth client credentials (read from the agy binary or
// the environment). Google refresh tokens do not rotate, so this cannot log
// agy out; the fresh access token is kept in memory only.
type QuotaFetcher struct {
	*plancache.Cache

	tokenPath string
	agyBinary string
	client    *http.Client
	logger    *slog.Logger

	apiBase       string
	tokenEndpoint string
	readCreds     func() (ids, secrets []string, err error)

	// Owned by the single in-flight update goroutine; Plan never reads them.
	accessToken string
	tokenExpiry time.Time
	project     string
	clientID    string
	clientSec   string
	credIDs     []string
	credSecrets []string
}

func NewQuotaFetcher(tokenPath, agyBinary string, logger *slog.Logger) *QuotaFetcher {
	f := &QuotaFetcher{
		tokenPath:     tokenPath,
		agyBinary:     agyBinary,
		client:        &http.Client{Timeout: requestTimeout},
		logger:        logger.With("module", "agy-quota"),
		apiBase:       apiBase,
		tokenEndpoint: tokenEndpoint,
	}
	f.readCreds = f.credsFromEnvOrBinary
	f.Cache = plancache.New("agy", f, f.logger)

	return f
}

// Fetch implements plancache.Fetcher: credentials, token, project, quota.
func (f *QuotaFetcher) Fetch(ctx context.Context, now time.Time) (domain.PlanUsage, error) {
	// The credential scan reads a ~170MB binary (slowly, under launchd's
	// background I/O priority); keep it outside the network budget and do
	// it once. A failure only matters if the token needs refreshing.
	if f.credIDs == nil {
		ids, secrets, err := f.readCreds()
		if err != nil {
			f.logger.Warn("agy oauth client credentials", "error", err)
		} else {
			f.credIDs, f.credSecrets = ids, secrets
		}
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	token, err := f.token(ctx, now)
	if err != nil {
		return domain.PlanUsage{}, fmt.Errorf("agy oauth token: %w", err)
	}

	if f.project == "" {
		project, err := f.loadProject(ctx, token)
		if err != nil {
			return domain.PlanUsage{}, err
		}

		f.project = project
	}

	body, err := f.post(ctx, token, "retrieveUserQuotaSummary", `{"project":`+jsonQuote(f.project)+`}`)
	if err != nil {
		return domain.PlanUsage{}, err
	}

	return parseQuota(body)
}

// jsonQuote quotes a string as a JSON literal.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)

	return string(b)
}

// token returns a valid access token: the on-disk one when still fresh,
// otherwise an in-memory one refreshed with agy's client credentials.
func (f *QuotaFetcher) token(ctx context.Context, now time.Time) (string, error) {
	if f.accessToken != "" && now.Before(f.tokenExpiry.Add(-expirySkew)) {
		return f.accessToken, nil
	}

	raw, err := os.ReadFile(f.tokenPath)
	if err != nil {
		return "", fmt.Errorf("%w: read token file: %w", plancache.ErrLogin, err)
	}

	var tf tokenFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return "", fmt.Errorf("%w: parse token file: %w", plancache.ErrLogin, err)
	}

	if tf.Token.AccessToken != "" && now.Before(tf.Token.Expiry.Add(-expirySkew)) {
		f.accessToken, f.tokenExpiry = tf.Token.AccessToken, tf.Token.Expiry

		return f.accessToken, nil
	}

	if tf.Token.RefreshToken == "" {
		return "", fmt.Errorf("%w: token expired and no refresh token in file", plancache.ErrLogin)
	}

	return f.refresh(ctx, tf.Token.RefreshToken, now)
}

func (f *QuotaFetcher) refresh(ctx context.Context, refreshToken string, now time.Time) (string, error) {
	if f.credIDs == nil {
		return "", errNoCreds
	}

	ids, secrets := f.credIDs, f.credSecrets

	// Prefer the pair that worked last time; otherwise try every
	// combination (the binary embeds a couple of each).
	pairs := make([][2]string, 0, len(ids)*len(secrets)+1)
	if f.clientID != "" {
		pairs = append(pairs, [2]string{f.clientID, f.clientSec})
	}

	for _, id := range ids {
		for _, sec := range secrets {
			pairs = append(pairs, [2]string{id, sec})
		}
	}

	var lastErr error = errNoCreds

	for _, p := range pairs {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
			"client_id":     {p[0]},
			"client_secret": {p[1]},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", userAgent)

		resp, err := f.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("refresh token: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()

		var tr struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			Error       string `json:"error"`
		}

		if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
			lastErr = fmt.Errorf("refresh with client %s: %s", shortID(p[0]), firstNonEmpty(tr.Error, resp.Status))

			continue
		}

		f.clientID, f.clientSec = p[0], p[1]
		f.accessToken = tr.AccessToken
		f.tokenExpiry = now.Add(time.Duration(tr.ExpiresIn) * time.Second)

		return f.accessToken, nil
	}

	return "", lastErr
}

func (f *QuotaFetcher) loadProject(ctx context.Context, token string) (string, error) {
	body, err := f.post(ctx, token, "loadCodeAssist", clientMetadata)
	if err != nil {
		return "", err
	}

	var lca struct {
		Project string `json:"cloudaicompanionProject"`
	}

	if err := json.Unmarshal(body, &lca); err != nil {
		return "", fmt.Errorf("decode loadCodeAssist: %w", err)
	}

	if lca.Project == "" {
		return "", errors.New("loadCodeAssist returned no cloudaicompanionProject")
	}

	return lca.Project, nil
}

func (f *QuotaFetcher) post(ctx context.Context, token, method, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.apiBase+":"+method, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", method, err)
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%s: %s: %w", method, resp.Status, plancache.ErrRateLimited)
	case resp.StatusCode == http.StatusUnauthorized:
		f.accessToken = "" // force a refresh next time

		return nil, fmt.Errorf("%s: %s", method, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: %s", method, resp.Status)
	}

	return out, nil
}

type quotaResponse struct {
	Groups []struct {
		DisplayName string `json:"displayName"`
		Buckets     []struct {
			BucketID          string    `json:"bucketId"`
			Window            string    `json:"window"`
			ResetTime         time.Time `json:"resetTime"`
			RemainingFraction float64   `json:"remainingFraction"`
		} `json:"buckets"`
	} `json:"groups"`
}

// parseQuota picks the Gemini model group and maps its 5h/weekly buckets to
// used percentages (the API reports the remaining fraction).
func parseQuota(body []byte) (domain.PlanUsage, error) {
	var qr quotaResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return domain.PlanUsage{}, fmt.Errorf("decode quota: %w", err)
	}

	plan := domain.UnknownPlanUsage()
	found := false

	for _, g := range qr.Groups {
		if !isGeminiGroup(g.Buckets) {
			continue
		}

		for _, b := range g.Buckets {
			limit := domain.Limit{
				Pct:      clampPct((1 - b.RemainingFraction) * percentMax),
				ResetsAt: b.ResetTime,
			}

			switch b.Window {
			case windowFiveHour:
				plan.FiveHour, found = limit, true
			case windowWeekly:
				plan.Weekly, found = limit, true
			}
		}
	}

	if !found {
		return plan, errors.New("quota response has no gemini 5h/weekly buckets")
	}

	return plan, nil
}

func isGeminiGroup(buckets []struct {
	BucketID          string    `json:"bucketId"`
	Window            string    `json:"window"`
	ResetTime         time.Time `json:"resetTime"`
	RemainingFraction float64   `json:"remainingFraction"`
}) bool {
	for _, b := range buckets {
		if strings.HasPrefix(b.BucketID, geminiBucketPrefix) {
			return true
		}
	}

	return false
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

// credsFromEnvOrBinary returns OAuth client ids/secrets: from the environment
// when set, otherwise scanned out of the agy binary (they are embedded as
// plain strings; agy uses them the same way).
func (f *QuotaFetcher) credsFromEnvOrBinary() ([]string, []string, error) {
	if id, sec := os.Getenv(EnvClientID), os.Getenv(EnvClientSecret); id != "" && sec != "" {
		return []string{id}, []string{sec}, nil
	}

	if f.agyBinary == "" {
		return nil, nil, errNoCreds
	}

	fh, err := os.Open(f.agyBinary)
	if err != nil {
		return nil, nil, fmt.Errorf("open agy binary: %w", err)
	}
	defer fh.Close()

	ids, secrets := scanClientCreds(fh)
	if len(ids) == 0 || len(secrets) == 0 {
		return nil, nil, errNoCreds
	}

	return ids, secrets, nil
}

// scanClientCreds streams a binary and collects distinct OAuth client ids and
// secrets. Chunks overlap so a match spanning a boundary is not lost.
func scanClientCreds(r io.Reader) (ids, secrets []string) {
	const (
		chunk   = 4 << 20
		overlap = 128
	)

	seenID := map[string]bool{}
	seenSec := map[string]bool{}
	buf := make([]byte, chunk+overlap)
	carry := 0

	for {
		n, err := io.ReadFull(r, buf[carry:])
		data := buf[:carry+n]

		for _, m := range clientIDRe.FindAll(data, -1) {
			seenID[string(m)] = true
		}

		for _, m := range clientSecretRe.FindAll(data, -1) {
			seenSec[string(m)] = true
		}

		if err != nil {
			break
		}

		carry = copy(buf, data[len(data)-overlap:])
	}

	for id := range seenID {
		ids = append(ids, id)
	}

	for sec := range seenSec {
		secrets = append(secrets, sec)
	}

	sort.Strings(ids)
	sort.Strings(secrets)

	return ids, secrets
}

func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i] + "-…"
	}

	return id
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
