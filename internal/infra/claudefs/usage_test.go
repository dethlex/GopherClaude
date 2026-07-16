package claudefs

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

func assistantLine(msgID, ts string, input, output, cacheCreation, cacheRead int) string {
	return `{"type":"assistant","timestamp":"` + ts + `","sessionId":"s1","message":{"id":"` + msgID + `","model":"claude-fable-5","usage":{` +
		`"input_tokens":` + strconv.Itoa(input) + `,` +
		`"output_tokens":` + strconv.Itoa(output) + `,` +
		`"cache_creation_input_tokens":` + strconv.Itoa(cacheCreation) + `,` +
		`"cache_read_input_tokens":` + strconv.Itoa(cacheRead) + `}}}` + "\n"
}

func TestTodayUsageDedupsAndFilters(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "-Users-x-proj")

	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 11, 15, 0, 0, 0, time.UTC)
	today := "2026-06-11T10:00:00.000Z"
	yesterday := "2026-06-10T10:00:00.000Z"

	content := assistantLine("msg_a", today, 100, 200, 10, 1000) +
		assistantLine("msg_a", today, 100, 200, 10, 1000) + // duplicate content block
		assistantLine("msg_b", today, 5, 7, 0, 0) +
		assistantLine("msg_old", yesterday, 999, 999, 999, 999) + // wrong day
		`{"type":"user","timestamp":"` + today + `"}` + "\n" +
		"not json at all\n"

	path := filepath.Join(project, "session1.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewUsageCollector(dir, time.UTC, discardLogger())

	got, err := c.TodayUsage(now)
	if err != nil {
		t.Fatalf("TodayUsage() error = %v", err)
	}

	want := domain.Usage{Input: 105, Output: 207, CacheCreation: 10, CacheRead: 1000}
	if got != want {
		t.Errorf("TodayUsage() = %+v, want %+v", got, want)
	}
}

func TestTodayUsageIncremental(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "-Users-x-proj")

	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 11, 15, 0, 0, 0, time.UTC)
	ts := "2026-06-11T10:00:00.000Z"
	path := filepath.Join(project, "session1.jsonl")

	if err := os.WriteFile(path, []byte(assistantLine("msg_a", ts, 10, 20, 0, 0)), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewUsageCollector(dir, time.UTC, discardLogger())

	if _, err := c.TodayUsage(now); err != nil {
		t.Fatalf("first TodayUsage() error = %v", err)
	}

	// Append: a new message plus a partial line still being written.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(assistantLine("msg_b", ts, 1, 2, 0, 0) + `{"type":"assistant","par`); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := c.TodayUsage(now.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("second TodayUsage() error = %v", err)
	}

	want := domain.Usage{Input: 11, Output: 22}
	if got != want {
		t.Errorf("TodayUsage() = %+v, want %+v", got, want)
	}
}

func TestTodayUsageResetsOnNewDay(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "-Users-x-proj")

	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 6, 11, 23, 0, 0, 0, time.UTC)
	path := filepath.Join(project, "session1.jsonl")

	if err := os.WriteFile(path, []byte(assistantLine("msg_a", "2026-06-11T22:00:00.000Z", 10, 20, 0, 0)), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewUsageCollector(dir, time.UTC, discardLogger())

	if _, err := c.TodayUsage(day1); err != nil {
		t.Fatal(err)
	}

	day2 := time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC)

	got, err := c.TodayUsage(day2)
	if err != nil {
		t.Fatal(err)
	}

	if got != (domain.Usage{}) {
		t.Errorf("after day rollover TodayUsage() = %+v, want zero", got)
	}
}
