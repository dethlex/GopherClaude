package codexfs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	threadDesktop  = "01a080d2-835a-7d12-8152-014c6160c47c"
	threadReview   = "0199aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	threadNoFile   = "0199ffff-0000-4111-8222-333333333333"
	threadTerminal = "0199bbbb-1111-4222-8333-444444444444"
)

// lsof -F pn over thread-writer-locks: the Desktop app-server (34530) holds
// two thread locks plus Codex's coordination lock, a terminal TUI (4242)
// holds one, and something (99999) has the directory itself open.
const lsofLocks = "p34530\n" +
	"n/Users/x/.codex/thread-writer-locks/.coordination.lock\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadDesktop + ".lock\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadReview + ".lock\n" +
	"p4242\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadTerminal + ".lock\n" +
	"p99999\n" +
	"n/Users/x/.codex/thread-writer-locks\n"

// writeRollout creates sessions/YYYY/MM/DD/rollout-<date>T10-00-00-<id>.jsonl
// holding the given lines and returns its path.
func writeRollout(t *testing.T, sessionsDir, date, id string, lines ...string) string {
	t.Helper()

	dir := filepath.Join(sessionsDir, date[:4], date[5:7], date[8:10])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "rollout-"+date+"T10-00-00-"+id+".jsonl")

	content := ""
	for _, l := range lines {
		content += l + "\n"
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// metaLine is a session_meta record as Codex writes it (fields the badge
// ignores included, so the decoder is exercised on the real shape).
func metaLine(ts, id, cwd, source string) string {
	return `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"` + id + `","timestamp":"` + ts +
		`","cwd":"` + cwd + `","originator":"codex-tui","cli_version":"0.150.1","source":` + source +
		`,"model_provider":"openai"}}`
}

func TestParseLockHolders(t *testing.T) {
	got := parseLockHolders([]byte(lsofLocks))

	// Sorted by pid, then thread id, whatever order lsof printed them in.
	want := []lockHolder{
		{pid: 4242, threadID: threadTerminal},
		{pid: 34530, threadID: threadReview},
		{pid: 34530, threadID: threadDesktop},
	}

	if len(got) != len(want) {
		t.Fatalf("holders = %+v, want %+v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("holders[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSessionRegistryBuildsAndCaches(t *testing.T) {
	sessionsDir := t.TempDir()

	// The Desktop thread started four days ago: its rollout sits under
	// that date, not today's.
	writeRollout(t, sessionsDir, "2026-09-08", threadDesktop,
		metaLine("2026-09-08T11:42:15.387Z", threadDesktop, "/Users/x/proj-a", `"vscode"`))
	writeRollout(t, sessionsDir, "2026-09-12", threadReview,
		metaLine("2026-09-12T09:00:00.000Z", threadReview, "/Users/x/proj-a", `{"subagent":"review"}`))
	writeRollout(t, sessionsDir, "2026-09-12", threadTerminal,
		metaLine("2026-09-12T09:30:00.000Z", threadTerminal, "/Users/x/proj-b", `"cli"`))

	calls := 0
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	r := NewSessionRegistry("/locks", sessionsDir, discardLogger())
	r.now = func() time.Time { return now }
	r.runLsof = func(args ...string) ([]byte, error) {
		calls++

		return []byte(lsofLocks), nil
	}

	sessions, err := r.Sessions()
	if err != nil {
		t.Fatal(err)
	}

	// The review sub-agent is skipped; the terminal thread (lower pid) comes first.
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want 2", sessions)
	}

	term := sessions[0]
	if term.Provider != domain.ProviderCodex || term.PID != 4242 || term.ID != threadTerminal || term.Dir != "/Users/x/proj-b" {
		t.Errorf("sessions[0] = %+v", term)
	}

	desk := sessions[1]
	wantStart := time.Date(2026, 9, 8, 11, 42, 15, 387_000_000, time.UTC)

	if desk.PID != 34530 || desk.ID != threadDesktop || desk.Dir != "/Users/x/proj-a" || !desk.StartedAt.Equal(wantStart) {
		t.Errorf("sessions[1] = %+v, want the Desktop thread started %v", desk, wantStart)
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

func TestSessionRegistrySkipsThreadWithoutRollout(t *testing.T) {
	sessionsDir := t.TempDir()
	writeRollout(t, sessionsDir, "2026-09-12", threadTerminal,
		metaLine("2026-09-12T09:30:00.000Z", threadTerminal, "/Users/x/proj-b", `"cli"`))

	r := NewSessionRegistry("/locks", sessionsDir, discardLogger())
	r.runLsof = func(...string) ([]byte, error) {
		return []byte("p4242\nn/Users/x/.codex/thread-writer-locks/" + threadTerminal + ".lock\n" +
			"p4343\nn/Users/x/.codex/thread-writer-locks/" + threadNoFile + ".lock\n"), nil
	}

	sessions, err := r.Sessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 1 || sessions[0].ID != threadTerminal {
		t.Errorf("sessions = %+v, want only the thread that has a rollout", sessions)
	}
}

func TestSessionRegistryEmptyWhenNothingHeld(t *testing.T) {
	r := NewSessionRegistry("/locks", t.TempDir(), discardLogger())
	r.runLsof = func(...string) ([]byte, error) { return nil, nil }

	sessions, err := r.Sessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none", sessions)
	}
}
