package badge

import (
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
			{Name: "ClaudeControl", Phase: domain.PhaseWaitingPermission, Minutes: 5, CtxTokens: 412000},
			{Provider: domain.ProviderAntigravity, Name: "rotator", Phase: domain.PhaseWorking, CtxTokens: 73000},
		},
		Agy: domain.ProviderStats{
			Chats:   2,
			Waiting: 1,
			Plan: domain.PlanUsage{
				FiveHour:   domain.Limit{Pct: 22, ResetsAt: now.Add(4*time.Hour + 41*time.Minute)},
				Weekly:     domain.Limit{Pct: 18, ResetsAt: now.Add(6*24*time.Hour + 4*time.Hour)},
				CreditsPct: domain.UnknownPct,
			},
			Prompts: 7,
		},
	}

	got := Encode(snap, now)
	want := "CC4|3|1|36|3h|1.4h|17|2d|65|32.66/50|86508|705246|ClaudeControl PERM" +
		"|ClaudeControl~P~5~412000~C;rotator~W~0~73000~A" +
		"|2|1|22|4h|18|6d|7"

	if got != want {
		t.Errorf("Encode() =\n%q\nwant\n%q", got, want)
	}
}

func TestEncodeUnknownPlan(t *testing.T) {
	snap := domain.Snapshot{
		Plan: domain.UnknownPlanUsage(),
		Agy:  domain.ProviderStats{Plan: domain.UnknownPlanUsage()},
	}

	got := Encode(snap, time.Now())
	want := "CC4|0|0|-1|||-1||-1||0|0||" + "|0|0|-1||-1||0"

	if got != want {
		t.Errorf("Encode() = %q, want %q", got, want)
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
	want := "we/ird/name/he~I~0~0~C"

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
		{"non ascii replaced", "проект PERM", strings.Repeat("?", 6) + " PERM"},
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
