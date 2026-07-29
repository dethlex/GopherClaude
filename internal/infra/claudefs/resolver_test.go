package claudefs

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	resolverCWD       = "/Users/x/proj"
	resolverSessionID = "sess-resolve"
)

// resolverFixture builds a Resolver over a temp events log and transcript.
func resolverFixture(t *testing.T, transcript string, events []string) *Resolver {
	t.Helper()

	dir := t.TempDir()

	writeTranscript(t, dir, resolverCWD, resolverSessionID, transcript)

	eventsPath := filepath.Join(dir, "events.jsonl")

	content := ""
	for _, e := range events {
		content += e + "\n"
	}

	if err := os.WriteFile(eventsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return NewResolver(NewEventLog(eventsPath), NewTranscriptDir(dir), discardLogger())
}

func hookEvent(ts time.Time, name, notify string) string {
	return `{"ts":` + strconv.FormatInt(ts.Unix(), 10) +
		`,"event":{"session_id":"` + resolverSessionID +
		`","hook_event_name":"` + name +
		`","notification_type":"` + notify + `"}}`
}

const (
	turnFinished = `{"type":"assistant","message":{"stop_reason":"end_turn"}}` + "\n"
	toolRunning  = `{"type":"assistant","message":{"stop_reason":"tool_use"}}` + "\n"
)

func resolveOne(t *testing.T, r *Resolver, status string, now time.Time) domain.SessionState {
	t.Helper()

	states := r.ResolveAll([]domain.Session{
		{ID: resolverSessionID, Dir: resolverCWD, Status: status},
	}, now)

	if len(states) != 1 {
		t.Fatalf("ResolveAll returned %d states, want 1", len(states))
	}

	return states[0]
}

// A stale PostToolUse must not pin a finished session to "working": Claude
// Desktop sessions never emit Stop, so without an expiry the badge spinner
// would spin forever.
func TestResolverStaleWorkingEventFallsBackToTranscript(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	stale := now.Add(-5 * time.Minute)

	r := resolverFixture(t, turnFinished, []string{hookEvent(stale, "PostToolUse", "")})

	got := resolveOne(t, r, "", now)
	if got.Phase != domain.PhaseWaitingInput {
		t.Errorf("Phase = %v, want PhaseWaitingInput (transcript says the turn ended)", got.Phase)
	}
}

// A fresh working event still wins — it is the fastest signal that work began.
func TestResolverFreshWorkingEventWins(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * time.Second)

	r := resolverFixture(t, turnFinished, []string{hookEvent(fresh, "PostToolUse", "")})

	got := resolveOne(t, r, "", now)
	if got.Phase != domain.PhaseWorking {
		t.Errorf("Phase = %v, want PhaseWorking", got.Phase)
	}
}

// Waiting events never expire: a permission dialog can sit for hours, and the
// transcript cannot tell one from a running tool.
func TestResolverOldPermissionEventStillWins(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)

	r := resolverFixture(t, toolRunning, []string{hookEvent(old, "Notification", "permission_prompt")})

	got := resolveOne(t, r, "", now)
	if got.Phase != domain.PhaseWaitingPermission {
		t.Errorf("Phase = %v, want PhaseWaitingPermission", got.Phase)
	}

	if got.Reason != reasonPermission {
		t.Errorf("Reason = %q, want %q", got.Reason, reasonPermission)
	}
}

// Both registry statuses Claude Code writes for an unoccupied cli session mean
// "waiting", even when the transcript tail looks busy.
func TestResolverRegistryStatuses(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)

	for _, status := range []string{statusIdle, statusWaiting} {
		t.Run(status, func(t *testing.T) {
			r := resolverFixture(t, toolRunning, nil)

			got := resolveOne(t, r, status, now)
			if got.Phase != domain.PhaseWaitingInput {
				t.Errorf("Phase = %v, want PhaseWaitingInput for status %q", got.Phase, status)
			}
		})
	}
}

func TestEventAuthoritative(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		phase domain.Phase
		at    time.Time
		want  bool
	}{
		{"fresh working", domain.PhaseWorking, now.Add(-time.Second), true},
		{"working at the edge", domain.PhaseWorking, now.Add(-workingEventTTL), true},
		{"expired working", domain.PhaseWorking, now.Add(-workingEventTTL - time.Second), false},
		{"ancient waiting input", domain.PhaseWaitingInput, now.Add(-24 * time.Hour), true},
		{"ancient waiting permission", domain.PhaseWaitingPermission, now.Add(-24 * time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventAuthoritative(tt.phase, tt.at, now); got != tt.want {
				t.Errorf("eventAuthoritative = %v, want %v", got, tt.want)
			}
		})
	}
}
