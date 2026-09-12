package codexfs

import (
	"fmt"
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

const (
	threadDesktop  = "01a080d2-835a-7d12-8152-014c6160c47c"
	threadReview   = "0199aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	threadNoFile   = "0199ffff-0000-4111-8222-333333333333"
	threadTerminal = "0199bbbb-1111-4222-8333-444444444444"
)

// lsof -F pn over thread-writer-locks, in the real shape (a descriptor line
// precedes every name): the Desktop app-server (34530) holds two thread
// locks plus Codex's coordination lock, a terminal TUI (4242) holds one lock
// through two descriptors, and a shell (99999) sits in the directory itself.
const lsofLocks = "p34530\n" +
	"f35u\n" +
	"n/Users/x/.codex/thread-writer-locks/.coordination.lock\n" +
	"f36u\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadDesktop + ".lock\n" +
	"f37u\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadReview + ".lock\n" +
	"p4242\n" +
	"f12u\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadTerminal + ".lock\n" +
	"f13u\n" +
	"n/Users/x/.codex/thread-writer-locks/" + threadTerminal + ".lock\n" +
	"p99999\n" +
	"fcwd\n" +
	"n/Users/x/.codex/thread-writer-locks\n"

// appendTo adds raw bytes to an existing rollout, the way Codex appends.
func appendTo(t *testing.T, path, s string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	return info.Size()
}

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

func eventLine(ts, typ, extra string) string {
	if extra != "" {
		extra = "," + extra
	}

	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"` + typ + `"` + extra + `}}`
}

func userLine(ts, text string) string {
	return `{"timestamp":"` + ts + `","type":"response_item","payload":{"type":"message","role":"user",` +
		`"content":[{"type":"input_text","text":` + strconv.Quote(text) + `}]}}`
}

// tokenCountExtra is the payload of a real token_count event minus the type.
const tokenCountExtra = `"info":{"total_token_usage":{"input_tokens":2656639,"total_tokens":2674845},` +
	`"last_token_usage":{"input_tokens":152120,"cached_input_tokens":151168,"output_tokens":1537,"total_tokens":153657},` +
	`"model_context_window":258400},"rate_limits":{"limit_id":"codex","primary":{"used_percent":17.0,"window_minutes":10080,"resets_at":1789472541},"secondary":null,"plan_type":"team"}`

func TestRolloutDirInspect(t *testing.T) {
	sessionsDir := t.TempDir()
	meta := metaLine("2026-09-12T09:00:00.000Z", "id", "/Users/x/p", `"cli"`)

	tests := []struct {
		name    string
		lines   []string
		want    domain.Phase
		wantCtx uint64
	}{
		{
			name: "turn running",
			lines: []string{meta,
				eventLine("2026-09-12T09:01:00.000Z", "task_complete", ""),
				userLine("2026-09-12T09:02:00.000Z", "next"),
				eventLine("2026-09-12T09:02:01.000Z", "task_started", ""),
				eventLine("2026-09-12T09:02:05.000Z", "token_count", tokenCountExtra),
			},
			want:    domain.PhaseWorking,
			wantCtx: 153657,
		},
		{
			name: "turn over",
			lines: []string{meta,
				eventLine("2026-09-12T09:01:00.000Z", "task_started", ""),
				eventLine("2026-09-12T09:01:05.000Z", "token_count", tokenCountExtra),
				eventLine("2026-09-12T09:01:06.000Z", "task_complete", ""),
			},
			want:    domain.PhaseWaitingInput,
			wantCtx: 153657,
		},
		{
			name: "interrupted",
			lines: []string{meta,
				eventLine("2026-09-12T09:01:00.000Z", "task_started", ""),
				eventLine("2026-09-12T09:01:06.000Z", "turn_aborted", ""),
			},
			want: domain.PhaseWaitingInput,
		},
		{
			name:  "never ran",
			lines: []string{meta},
			want:  domain.PhaseWaitingInput,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := fmt.Sprintf("0199cccc-0000-4000-8000-%012d", i)
			writeRollout(t, sessionsDir, "2026-09-12", id, tt.lines...)

			got, ctx, err := NewRolloutDir(sessionsDir).Inspect(domain.Session{ID: id})
			if err != nil {
				t.Fatal(err)
			}

			if got != tt.want || ctx != tt.wantCtx {
				t.Errorf("Inspect = %v/%d, want %v/%d", got, ctx, tt.want, tt.wantCtx)
			}
		})
	}
}

func TestRolloutDirInspectMissingRollout(t *testing.T) {
	got, ctx, err := NewRolloutDir(t.TempDir()).Inspect(domain.Session{ID: threadNoFile})
	if err != nil || got != domain.PhaseWaitingInput || ctx != 0 {
		t.Errorf("Inspect(missing) = %v/%d/%v, want waiting input, 0, nil", got, ctx, err)
	}
}

func TestPromptCounterCountsTodaysUserMessages(t *testing.T) {
	sessionsDir := t.TempDir()
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)

	// Started yesterday, used today: lives under yesterday's date.
	old := writeRollout(t, sessionsDir, "2026-09-11", threadDesktop,
		metaLine("2026-09-11T20:00:00.000Z", threadDesktop, "/Users/x/a", `"cli"`),
		userLine("2026-09-11T20:00:01.000Z", "yesterday's prompt"),
		userLine("2026-09-12T08:00:00.000Z", "<environment_context>injected</environment_context>"),
		userLine("2026-09-12T08:00:01.000Z", "today one"),
		eventLine("2026-09-12T08:00:02.000Z", "task_started", ""),
	)
	fresh := writeRollout(t, sessionsDir, "2026-09-12", threadTerminal,
		metaLine("2026-09-12T10:00:00.000Z", threadTerminal, "/Users/x/b", `"cli"`),
		userLine("2026-09-12T10:00:01.000Z", "today two"),
		userLine("2026-09-12T10:05:00.000Z", "today three"),
	)
	// Untouched since yesterday: never opened at all.
	stale := writeRollout(t, sessionsDir, "2026-09-10", threadReview,
		metaLine("2026-09-10T10:00:00.000Z", threadReview, "/Users/x/c", `"cli"`),
		userLine("2026-09-12T10:00:01.000Z", "would count if the file were read"),
	)

	for path, mtime := range map[string]time.Time{old: now, fresh: now, stale: now.Add(-30 * time.Hour)} {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	c := NewPromptCounter(sessionsDir, time.UTC, discardLogger())

	if got := c.PromptsToday(now); got != 3 {
		t.Errorf("PromptsToday = %d, want 3", got)
	}

	// A new prompt lands; within the TTL the cached count is still served.
	later := writeRollout(t, sessionsDir, "2026-09-12", threadNoFile,
		metaLine("2026-09-12T11:00:00.000Z", threadNoFile, "/Users/x/d", `"cli"`),
		userLine("2026-09-12T11:00:01.000Z", "later"))

	if err := os.Chtimes(later, now, now); err != nil {
		t.Fatal(err)
	}

	if got := c.PromptsToday(now.Add(10 * time.Second)); got != 3 {
		t.Errorf("PromptsToday within TTL = %d, want cached 3", got)
	}

	if got := c.PromptsToday(now.Add(promptsTTL + time.Second)); got != 4 {
		t.Errorf("PromptsToday after TTL = %d, want 4", got)
	}
}

// A Desktop thread's rollout runs to 100 MB; re-parsing it every minute
// would stall the frame loop, so the counter must remember how far it read
// each file and parse only what Codex appended since.
func TestPromptCounterReadsIncrementally(t *testing.T) {
	sessionsDir := t.TempDir()
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)

	path := writeRollout(t, sessionsDir, "2026-09-12", threadTerminal,
		metaLine("2026-09-12T10:00:00.000Z", threadTerminal, "/Users/x/b", `"cli"`),
		userLine("2026-09-12T10:00:01.000Z", "one"),
		userLine("2026-09-12T10:05:00.000Z", "two"),
	)

	touch := func(at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	touch(now)

	c := NewPromptCounter(sessionsDir, time.UTC, discardLogger())

	if got := c.PromptsToday(now); got != 2 {
		t.Fatalf("PromptsToday = %d, want 2", got)
	}

	if cur := c.files[path]; cur == nil || cur.cur.Offset != fileSize(t, path) {
		t.Fatalf("cursor after the first pass = %+v, want offset %d", cur, fileSize(t, path))
	}

	// Codex appends a full line and then the beginning of another: the
	// complete line counts, the torn one waits for its newline and must not
	// move the cursor past its start.
	appendTo(t, path, userLine("2026-09-12T10:10:00.000Z", "three")+"\n")
	completeSize := fileSize(t, path)
	appendTo(t, path, `{"timestamp":"2026-09-12T10:11:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fo`)
	touch(now)

	at := now.Add(promptsTTL + time.Second)
	if got := c.PromptsToday(at); got != 3 {
		t.Errorf("PromptsToday after append = %d, want 3", got)
	}

	if cur := c.files[path]; cur.cur.Offset != completeSize {
		t.Errorf("cursor after a torn line = %d, want %d (start of the torn line)", cur.cur.Offset, completeSize)
	}

	appendTo(t, path, `ur"}]}}`+"\n")
	touch(at)

	at = at.Add(promptsTTL + time.Second)
	if got := c.PromptsToday(at); got != 4 {
		t.Errorf("PromptsToday after the line completed = %d, want 4", got)
	}

	// A file that shrank (rewritten, rotated) is read from the start again.
	if err := os.WriteFile(path, []byte(metaLine("2026-09-12T10:00:00.000Z", threadTerminal, "/Users/x/b", `"cli"`)+"\n"+
		userLine("2026-09-12T10:20:00.000Z", "only")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(at)

	at = at.Add(promptsTTL + time.Second)
	if got := c.PromptsToday(at); got != 1 {
		t.Errorf("PromptsToday after truncation = %d, want 1", got)
	}

	// Midnight: yesterday's prompts drop out, but the cursor is kept, so a
	// prompt appended today is found without re-reading the file.
	tomorrow := time.Date(2026, 9, 13, 0, 30, 0, 0, time.UTC)
	if got := c.PromptsToday(tomorrow); got != 0 {
		t.Errorf("PromptsToday on the next day = %d, want 0", got)
	}

	appendTo(t, path, userLine("2026-09-13T00:31:00.000Z", "morning")+"\n")
	touch(tomorrow)

	if got := c.PromptsToday(tomorrow.Add(promptsTTL + time.Second)); got != 1 {
		t.Errorf("PromptsToday after a next-day prompt = %d, want 1", got)
	}
}

func TestPromptCounterMissingDir(t *testing.T) {
	c := NewPromptCounter(filepath.Join(t.TempDir(), "absent"), time.UTC, discardLogger())

	if got := c.PromptsToday(time.Now()); got != 0 {
		t.Errorf("PromptsToday(missing dir) = %d, want 0", got)
	}
}
