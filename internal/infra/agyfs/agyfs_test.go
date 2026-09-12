package agyfs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const lsofPresence = "p11238\nn/Users/x/.gemini/antigravity-cli/presence/16a94c26-3837.lock\n" +
	"p18889\nn/Users/x/.gemini/antigravity-cli/presence/88c54ac9-d9b3.lock\n" +
	"p99999\nn/Users/x/.gemini/antigravity-cli/presence\n" // a directory opener, not a lock

func TestSessionRegistryBuildsAndCaches(t *testing.T) {
	calls := 0

	r := NewSessionRegistry("/presence", discardLogger())
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	r.Run = func(args ...string) ([]byte, error) {
		calls++

		if args[0] == "-F" { // presence walk
			return []byte(lsofPresence), nil
		}

		return []byte("p11238\nfcwd\nn/Users/x/proj-a\np18889\nfcwd\nn/Users/x/proj-b\n"), nil
	}

	sessions, err := r.Sessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}

	first := sessions[0]
	if first.Provider != domain.ProviderAntigravity || first.PID != 11238 ||
		first.ID != "16a94c26-3837" || first.Dir != "/Users/x/proj-a" {
		t.Errorf("sessions[0] = %+v", first)
	}

	if sessions[1].PID != 18889 || sessions[1].Dir != "/Users/x/proj-b" {
		t.Errorf("sessions[1] = %+v", sessions[1])
	}

	// Within the TTL the registry must not shell out again.
	before := calls

	if _, err := r.Sessions(); err != nil {
		t.Fatal(err)
	}

	if calls != before {
		t.Errorf("lsof called %d more times within TTL", calls-before)
	}

	now = now.Add(registryTTL + time.Second)

	if _, err := r.Sessions(); err != nil {
		t.Fatal(err)
	}

	if calls == before {
		t.Error("registry did not refresh after TTL")
	}
}

func TestHistoryPromptsToday(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")

	now := time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC)
	today := now.Add(-2 * time.Hour).UnixMilli()
	yesterday := now.Add(-30 * time.Hour).UnixMilli()

	content := `{"timestamp":` + strconv.FormatInt(today, 10) + `,"display":"a"}` + "\n" +
		`{"timestamp":` + strconv.FormatInt(today, 10) + `,"display":"b"}` + "\n" +
		`{"timestamp":` + strconv.FormatInt(yesterday, 10) + `,"display":"c"}` + "\n" +
		"garbage\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := NewHistory(path, time.UTC, discardLogger()).PromptsToday(now); got != 2 {
		t.Errorf("PromptsToday = %d, want 2", got)
	}

	if got := NewHistory(filepath.Join(dir, "absent.jsonl"), time.UTC, discardLogger()).PromptsToday(now); got != 0 {
		t.Errorf("PromptsToday(missing) = %d, want 0", got)
	}
}

func TestConversationDirInspect(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC)

	c := NewConversationDir(dir)
	c.now = func() time.Time { return now }

	fresh := filepath.Join(dir, "fresh.db")
	stale := filepath.Join(dir, "stale.db")

	for _, p := range []string{fresh, stale} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Chtimes(fresh, now, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(stale, now, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		id   string
		want domain.Phase
	}{
		{"fresh", domain.PhaseWorking},
		{"stale", domain.PhaseWaitingInput},
		{"missing", domain.PhaseWaitingInput},
	}

	for _, tt := range tests {
		got, ctx, err := c.Inspect(domain.Session{ID: tt.id})
		if err != nil {
			t.Fatalf("%s: %v", tt.id, err)
		}

		if got != tt.want || ctx != 0 {
			t.Errorf("%s: phase=%v ctx=%d, want %v/0", tt.id, got, ctx, tt.want)
		}
	}
}
