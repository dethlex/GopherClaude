package usecase

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

type (
	fakeSessions struct{ sessions []domain.Session }
	fakeUsage    struct{ usage domain.Usage }
	fakePlan     struct{ plan domain.PlanUsage }
	fakePhases   struct{ states []domain.SessionState }
)

func (f fakeSessions) Sessions() ([]domain.Session, error) { return f.sessions, nil }

func (f fakeUsage) TodayUsage(time.Time) (domain.Usage, error) { return f.usage, nil }

func (f fakePlan) Plan(time.Time) domain.PlanUsage { return f.plan }

func (f fakePhases) ResolveAll([]domain.Session, time.Time) []domain.SessionState { return f.states }

func TestMonitorSnapshot(t *testing.T) {
	sessions := []domain.Session{
		{ID: "s1", Dir: "/Users/x/alpha"},
		{ID: "s2", Dir: "/Users/x/beta"},
		{ID: "s3", Dir: "/Users/x/gamma", PID: 4242},
	}

	base := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

	states := []domain.SessionState{
		{Session: sessions[0], Phase: domain.PhaseWorking},
		{Session: sessions[1], Phase: domain.PhaseWaitingInput, Reason: "INPUT", Since: base},
		{Session: sessions[2], Phase: domain.PhaseWaitingPermission, Reason: "PERM", Since: base.Add(time.Minute)},
	}

	monitor := NewMonitor(
		fakeSessions{sessions},
		fakeUsage{domain.Usage{Input: 10, Output: 20}},
		fakePlan{domain.PlanUsage{FiveHour: domain.Limit{Pct: 36}}},
		fakePhases{states},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	got := monitor.Snapshot(base.Add(2 * time.Minute))

	if got.Plan.FiveHour.Pct != 36 {
		t.Errorf("Plan.FiveHour.Pct = %d, want 36", got.Plan.FiveHour.Pct)
	}

	if got.Chats != 3 {
		t.Errorf("Chats = %d, want 3", got.Chats)
	}

	if got.Waiting != 2 {
		t.Errorf("Waiting = %d, want 2", got.Waiting)
	}

	if got.Message != "gamma PERM" {
		t.Errorf("Message = %q, want %q (most recent waiting session)", got.Message, "gamma PERM")
	}

	if got.Usage.Input != 10 || got.Usage.Output != 20 {
		t.Errorf("Usage = %+v", got.Usage)
	}

	if len(got.Sessions) != 3 {
		t.Fatalf("Sessions = %d rows, want 3", len(got.Sessions))
	}

	// Permission waits sort first, then input waits, then working.
	if got.Sessions[0].Name != "gamma" || got.Sessions[0].Phase != domain.PhaseWaitingPermission {
		t.Errorf("Sessions[0] = %+v, want gamma PERM", got.Sessions[0])
	}

	if got.Sessions[1].Name != "beta" || got.Sessions[2].Name != "alpha" {
		t.Errorf("Sessions order = %s, %s; want beta, alpha",
			got.Sessions[1].Name, got.Sessions[2].Name)
	}

	// gamma waits since base+1m, snapshot at base+2m => 1 minute.
	if got.Sessions[0].Minutes != 1 {
		t.Errorf("Sessions[0].Minutes = %d, want 1", got.Sessions[0].Minutes)
	}

	// Focus target is the banner session (newest waiting = gamma).
	if got.Focus == nil {
		t.Fatal("Focus = nil, want gamma")
	}

	if got.Focus.Dir != "/Users/x/gamma" || got.Focus.PID != 4242 {
		t.Errorf("Focus = %+v, want gamma/4242", *got.Focus)
	}

	// FocusTargets line up with Sessions row for row.
	if len(got.FocusTargets) != len(got.Sessions) {
		t.Fatalf("FocusTargets has %d entries, want %d", len(got.FocusTargets), len(got.Sessions))
	}

	if got.FocusTargets[0].Dir != "/Users/x/gamma" || got.FocusTargets[0].PID != 4242 {
		t.Errorf("FocusTargets[0] = %+v, want gamma/4242", got.FocusTargets[0])
	}
}

func TestMonitorSnapshotQuiet(t *testing.T) {
	monitor := NewMonitor(
		fakeSessions{},
		fakeUsage{},
		fakePlan{},
		fakePhases{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	got := monitor.Snapshot(time.Now())

	if got.Chats != 0 || got.Waiting != 0 || got.Message != "" {
		t.Errorf("Snapshot() = %+v, want zero state", got)
	}

	if got.Focus != nil {
		t.Errorf("Focus = %+v, want nil when nothing waits", *got.Focus)
	}
}
