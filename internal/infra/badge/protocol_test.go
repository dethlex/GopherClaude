package badge

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func TestEncode(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)

	snap := domain.Snapshot{
		Chats:   3,
		Waiting: 1,
		Usage: domain.Usage{
			Input:  86508,
			Output: 705246,
		},
		Plan: domain.PlanUsage{
			FiveHour:    domain.Limit{Pct: 36, ResetsAt: now.Add(3*time.Hour + 10*time.Minute)},
			Weekly:      domain.Limit{Pct: 17, ResetsAt: now.Add(50 * time.Hour)},
			CreditsPct:  65,
			CreditsText: "32.66/50",
			FiveHourETA: 84 * time.Minute,
		},
		Message: "ClaudeControl PERM",
		Sessions: []domain.SessionBrief{
			{Name: "ClaudeControl", Title: "ClaudeControl", Path: "/Users/x/GolandProjects/ClaudeControl", Phase: domain.PhaseWaitingPermission, Minutes: 5, CtxTokens: 412000},
			{Provider: domain.ProviderAntigravity, Name: "rotator", Title: "fix the rotator retries", Path: "/Users/x/rotator", Phase: domain.PhaseWorking, CtxTokens: 73000},
			{Provider: domain.ProviderCodex, Name: "api", Phase: domain.PhaseWaitingInput, Minutes: 2, CtxTokens: 9000},
		},
		Extras: []domain.ProviderStats{
			{
				Provider: domain.ProviderAntigravity,
				Chats:    2,
				Waiting:  1,
				Plan: domain.PlanUsage{
					FiveHour:   domain.Limit{Pct: 22, ResetsAt: now.Add(4*time.Hour + 41*time.Minute)},
					Weekly:     domain.Limit{Pct: 18, ResetsAt: now.Add(6*24*time.Hour + 4*time.Hour)},
					CreditsPct: domain.UnknownPct,
				},
				Prompts: 7,
			},
			{
				Provider: domain.ProviderCodex,
				Chats:    1,
				Plan: domain.PlanUsage{
					FiveHour:   domain.Limit{Pct: domain.UnknownPct},
					Weekly:     domain.Limit{Pct: 17, ResetsAt: now.Add(6*24*time.Hour + 4*time.Hour)},
					CreditsPct: domain.UnknownPct,
				},
				Prompts: 3,
			},
		},
	}

	got := Encode(snap, now)
	want := "CC7|3|1|36|3h|1.4h|17|2d|65|32.66/50|86508|705246|ClaudeControl PERM" +
		"|ClaudeControl~P~5~412000~C~ClaudeControl~../ClaudeControl" +
		";rotator~W~0~73000~A~fix the rotator retries~/Users/x/rotator" +
		";api~I~2~9000~X~~" +
		"|A~2~1~22~4h~18~6d~7;X~1~0~-1~~17~6d~3"

	if got != want {
		t.Errorf("Encode() =\n%q\nwant\n%q", got, want)
	}
}

func TestEncodeUnknownPlan(t *testing.T) {
	snap := domain.Snapshot{Plan: domain.UnknownPlanUsage()}

	got := Encode(snap, time.Now())

	// No extras: Claude alone, the trailing field stays empty.
	want := "CC7|0|0|-1|||-1||-1||0|0|||"

	if got != want {
		t.Errorf("Encode() = %q, want %q", got, want)
	}
}

func TestEncodeSingleExtra(t *testing.T) {
	snap := domain.Snapshot{
		Plan:   domain.UnknownPlanUsage(),
		Extras: []domain.ProviderStats{{Provider: domain.ProviderCodex, Plan: domain.UnknownPlanUsage()}},
	}

	got := Encode(snap, time.Now())

	if !strings.HasSuffix(got, "|X~0~0~-1~~-1~~0") {
		t.Errorf("Encode() = %q, want a single Codex group with no separator around it", got)
	}
}

func TestFormatETA(t *testing.T) {
	tests := []struct {
		eta  time.Duration
		want string
	}{
		{0, ""},
		{-time.Minute, ""},
		{30 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{84 * time.Minute, "1.4h"},
		{150 * time.Minute, "2.5h"},
	}

	for _, tt := range tests {
		if got := formatETA(tt.eta); got != tt.want {
			t.Errorf("formatETA(%v) = %q, want %q", tt.eta, got, tt.want)
		}
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   domain.Command
		wantOK bool
	}{
		{"focus banner", "CMD focus", domain.Command{Name: "focus", Index: domain.NoIndex}, true},
		{"focus row", "CMD focus 3", domain.Command{Name: "focus", Index: 3}, true},
		{"trailing crlf", "CMD focus\r", domain.Command{Name: "focus", Index: domain.NoIndex}, true},
		{"negative index ignored", "CMD focus -1", domain.Command{Name: "focus", Index: domain.NoIndex}, true},
		{"echo is not a command", "ok chats=4 wait=2", domain.Command{}, false},
		{"empty payload", "CMD ", domain.Command{}, false},
		{"noise", "garbage", domain.Command{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseCommand(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ParseCommand(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}

			if ok && got != tt.want {
				t.Errorf("ParseCommand(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestEncodeSessionsSanitizesName(t *testing.T) {
	sessions := []domain.SessionBrief{
		{Name: "we|ird~name;here-and-too-long", Phase: domain.PhaseWaitingInput},
	}

	got := encodeSessions(sessions)
	want := "we/ird/name/he~I~0~0~C~~"

	if got != want {
		t.Errorf("encodeSessions() = %q, want %q", got, want)
	}
}

func TestFormatReset(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		limit domain.Limit
		want  string
	}{
		{"unknown", domain.Limit{Pct: domain.UnknownPct}, ""},
		{"no reset time", domain.Limit{Pct: 10}, ""},
		{"minutes", domain.Limit{Pct: 10, ResetsAt: now.Add(45 * time.Minute)}, "45m"},
		{"sub minute", domain.Limit{Pct: 10, ResetsAt: now.Add(20 * time.Second)}, "1m"},
		{"hours", domain.Limit{Pct: 10, ResetsAt: now.Add(3*time.Hour + 50*time.Minute)}, "3h"},
		{"days", domain.Limit{Pct: 10, ResetsAt: now.Add(49 * time.Hour)}, "2d"},
		{"past", domain.Limit{Pct: 10, ResetsAt: now.Add(-time.Minute)}, "now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatReset(tt.limit, now); got != tt.want {
				t.Errorf("formatReset() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pipe replaced", "a|b", "a/b"},
		{"cyrillic transliterated", "проект PERM", "proekt PERM"},
		{"other scripts replaced", "日本 PERM", "?? PERM"},
		{"truncated", strings.Repeat("x", 40), strings.Repeat("x", maxMsgLen)},
		{"plain kept", "ClaudeControl INPUT", "ClaudeControl INPUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeText(tt.in); got != tt.want {
				t.Errorf("sanitizeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestShortenPath(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/Users/x/proj", "/Users/x/proj"},
		{"/Users/lexis/go/src/t10s/abc", "/Users/lexis/go/src/t10s/abc"}, // exactly 28 bytes
		{"/Users/x/GolandProjects/ClaudeControl", "../ClaudeControl"},
		{"/opt/homebrew/Cellar/tinygo/0.41.1/src/machine", "../tinygo/0.41.1/src/machine"}, // 28 bytes: the first cut that fits
		{"/a/" + strings.Repeat("b", 40), ".." + strings.Repeat("b", 26)},
		{"", ""},
	}

	for _, c := range cases {
		got := shortenPath(c.path, maxPathLen)

		if got != c.want {
			t.Errorf("shortenPath(%q) = %q, want %q", c.path, got, c.want)
		}

		if len(got) > maxPathLen {
			t.Errorf("shortenPath(%q) = %d bytes, over %d", c.path, len(got), maxPathLen)
		}
	}
}

func TestEncodeSessionsShortensPathAndTitle(t *testing.T) {
	sessions := []domain.SessionBrief{
		{
			Name:  "t10s-36",
			Title: "this title is far too long for one banner line",
			Path:  "/Users/x/go/src/t10s/rtb-antifraud",
			Phase: domain.PhaseWorking,
		},
	}

	got := encodeSessions(sessions)
	want := "t10s-36~W~0~0~C~this title is far too long f~../go/src/t10s/rtb-antifraud"

	if got != want {
		t.Errorf("encodeSessions() = %q, want %q", got, want)
	}
}

func TestSanitizeTransliterates(t *testing.T) {
	cases := []struct{ in, want string }{
		{"надо заморозить Сервис", "nado zamorozit Servis"},
		{"Щётка, ёж и объём", "Shchyotka, yozh i obyom"},
		{"日本", "??"},
	}

	for _, c := range cases {
		if got := sanitize(c.in, 64); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// A multi-letter transliteration must not push the result past the
	// limit: the badge's row and banner widths are exact.
	if got := sanitize(strings.Repeat("щ", 10), maxNameLen); len(got) != maxNameLen {
		t.Errorf("sanitize(щ×10, %d) = %q (%d bytes)", maxNameLen, got, len(got))
	}

	if got := sanitizeUnbounded("путь/к/проекту"); got != "put/k/proektu" {
		t.Errorf("sanitizeUnbounded = %q, want %q", got, "put/k/proektu")
	}
}

// badgeLineBufSize mirrors lineBufSize in firmware/serial.go: a longer line
// is truncated on the badge and dropped as a bad frame, so the worst frame
// the host can build must fit with room to spare.
const badgeLineBufSize = 4096

func TestEncodeWorstCaseFitsBadgeBuffer(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("W", 64)

	fullPlan := domain.PlanUsage{
		FiveHour:    domain.Limit{Pct: 100, ResetsAt: now.Add(4*time.Hour + 59*time.Minute)},
		Weekly:      domain.Limit{Pct: 100, ResetsAt: now.Add(6*24*time.Hour + 23*time.Hour)},
		CreditsPct:  100,
		CreditsText: long,
		FiveHourETA: 99 * time.Hour,
	}

	snap := domain.Snapshot{
		Chats:   999,
		Waiting: 999,
		Usage:   domain.Usage{Input: math.MaxUint64, Output: math.MaxUint64},
		Plan:    fullPlan,
		Message: long,
		Extras: []domain.ProviderStats{
			{Provider: domain.ProviderAntigravity, Chats: 999, Waiting: 999, Plan: fullPlan, Prompts: 99999},
			{Provider: domain.ProviderCodex, Chats: 999, Waiting: 999, Plan: fullPlan, Prompts: 99999},
			{Provider: domain.ProviderCodex, Chats: 999, Waiting: 999, Plan: fullPlan, Prompts: 99999},
		},
	}

	for i := 0; i < domain.MaxSessionRows; i++ {
		snap.Sessions = append(snap.Sessions, domain.SessionBrief{
			Provider:  domain.ProviderCodex,
			Name:      long,
			Title:     long,
			Path:      "/" + strings.Repeat("p/", 40),
			Phase:     domain.PhaseWaitingPermission,
			Minutes:   999,
			CtxTokens: math.MaxUint64,
		})
	}

	line := Encode(snap, now) + "\n"

	if len(line) >= badgeLineBufSize {
		t.Errorf("worst-case frame = %d bytes, must stay under %d", len(line), badgeLineBufSize)
	}

	if len(line) > badgeLineBufSize*9/10 {
		t.Errorf("worst-case frame = %d bytes, less than 10%% headroom under %d", len(line), badgeLineBufSize)
	}
}

// A label that sanitizes to nothing (soft and hard signs only) still needs
// a row on the badge: an empty first field is skipped by the parser and
// every later focus index would point one row off.
func TestEncodeSessionsNeverSendsEmptyLabel(t *testing.T) {
	got := encodeSessions([]domain.SessionBrief{{Name: "ьъ", Phase: domain.PhaseWorking}})
	want := "?~W~0~0~C~~"

	if got != want {
		t.Errorf("encodeSessions(empty label) = %q, want %q", got, want)
	}
}
