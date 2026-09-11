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
	fakePrompts  struct{ n int }
)

func (f fakeSessions) Sessions() ([]domain.Session, error) { return f.sessions, nil }

func (f fakeUsage) TodayUsage(time.Time) (domain.Usage, error) { return f.usage, nil }

func (f fakePlan) Plan(time.Time) domain.PlanUsage { return f.plan }

func (f fakePhases) ResolveAll([]domain.Session, time.Time) []domain.SessionState { return f.states }

func (f fakePrompts) PromptsToday(time.Time) int { return f.n }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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

	monitor := NewMonitor(Sources{
		Claude: ProviderSources{
			Sessions: fakeSessions{sessions},
			Phases:   fakePhases{states},
			Plan:     fakePlan{domain.PlanUsage{FiveHour: domain.Limit{Pct: 36}}},
		},
		Usage: fakeUsage{domain.Usage{Input: 10, Output: 20}},
	}, testLogger())

	got := monitor.Snapshot(base.Add(2 * time.Minute))

	if got.Plan.FiveHour.Pct != 36 {
		t.Errorf("Plan.FiveHour.Pct = %d, want 36", got.Plan.FiveHour.Pct)
	}

	if got.Chats != 3 || got.Waiting != 2 {
		t.Errorf("Chats/Waiting = %d/%d, want 3/2", got.Chats, got.Waiting)
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
		t.Errorf("Sessions order = %s, %s; want beta, alpha", got.Sessions[1].Name, got.Sessions[2].Name)
	}

	// gamma waits since base+1m, snapshot at base+2m => 1 minute.
	if got.Sessions[0].Minutes != 1 {
		t.Errorf("Sessions[0].Minutes = %d, want 1", got.Sessions[0].Minutes)
	}

	if got.Focus == nil || got.Focus.Dir != "/Users/x/gamma" || got.Focus.PID != 4242 {
		t.Errorf("Focus = %+v, want gamma/4242", got.Focus)
	}

	if len(got.FocusTargets) != len(got.Sessions) || got.FocusTargets[0].PID != 4242 {
		t.Errorf("FocusTargets = %+v", got.FocusTargets)
	}

	// Without Antigravity wired, its block is explicitly "unknown", not 0%,
	// and the badge is told not to show its screens at all.
	if got.Agy.Chats != 0 || got.Agy.Plan.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Agy = %+v, want empty with unknown plan", got.Agy)
	}

	if len(got.Providers) != 1 || got.Providers[0] != domain.ProviderClaude {
		t.Errorf("Providers = %v, want Claude alone", got.Providers)
	}
}

func TestMonitorMergesAntigravity(t *testing.T) {
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	claudeSess := []domain.Session{{ID: "c1", Dir: "/Users/x/claude-proj", PID: 1}}
	claudeStates := []domain.SessionState{
		{Session: claudeSess[0], Phase: domain.PhaseWaitingInput, Reason: "INPUT", Since: base},
	}

	agySess := []domain.Session{
		{Provider: domain.ProviderAntigravity, ID: "a1", Dir: "/Users/x/agy-proj", PID: 2, Model: "gemini-3.7-flash-high"},
		{Provider: domain.ProviderAntigravity, ID: "a2", Dir: "/Users/x/agy-other", PID: 3},
	}
	agyStates := []domain.SessionState{
		{Session: agySess[0], Phase: domain.PhaseWaitingInput, Reason: "INPUT", Since: base.Add(5 * time.Minute)},
		{Session: agySess[1], Phase: domain.PhaseWorking},
	}

	monitor := NewMonitor(Sources{
		Claude: ProviderSources{Sessions: fakeSessions{claudeSess}, Phases: fakePhases{claudeStates}},
		Usage:  fakeUsage{},
		Agy: &ProviderSources{
			Sessions: fakeSessions{agySess},
			Phases:   fakePhases{agyStates},
			Plan:     fakePlan{domain.PlanUsage{FiveHour: domain.Limit{Pct: 22}, Weekly: domain.Limit{Pct: 18}}},
			Prompts:  fakePrompts{7},
		},
	}, testLogger())

	got := monitor.Snapshot(base.Add(10 * time.Minute))

	if got.Chats != 1 || got.Waiting != 1 {
		t.Errorf("Claude Chats/Waiting = %d/%d, want 1/1", got.Chats, got.Waiting)
	}

	if got.Agy.Chats != 2 || got.Agy.Waiting != 1 || got.Agy.Prompts != 7 || got.Agy.Plan.FiveHour.Pct != 22 {
		t.Errorf("Agy = %+v", got.Agy)
	}

	if len(got.Providers) != 2 || got.Providers[1] != domain.ProviderAntigravity {
		t.Errorf("Providers = %v, want Claude and Antigravity", got.Providers)
	}

	// The banner follows the newest wait regardless of provider.
	if got.Message != "agy-proj INPUT" || got.Focus == nil || got.Focus.PID != 2 {
		t.Errorf("Message=%q Focus=%+v, want the agy session", got.Message, got.Focus)
	}

	if len(got.Sessions) != 3 {
		t.Fatalf("Sessions = %d, want 3 merged rows", len(got.Sessions))
	}

	// Waiting rows first (older wait on top), working last; providers preserved.
	if got.Sessions[0].Name != "claude-proj" || got.Sessions[0].Provider != domain.ProviderClaude {
		t.Errorf("Sessions[0] = %+v", got.Sessions[0])
	}

	if got.Sessions[1].Name != "agy-proj" || got.Sessions[1].Provider != domain.ProviderAntigravity {
		t.Errorf("Sessions[1] = %+v", got.Sessions[1])
	}

	if got.Sessions[2].Phase != domain.PhaseWorking || got.FocusTargets[2].PID != 3 {
		t.Errorf("Sessions[2]/FocusTargets[2] = %+v / %+v", got.Sessions[2], got.FocusTargets[2])
	}
}

func TestMonitorSnapshotQuiet(t *testing.T) {
	monitor := NewMonitor(Sources{
		Claude: ProviderSources{Sessions: fakeSessions{}, Phases: fakePhases{}},
		Usage:  fakeUsage{},
	}, testLogger())

	got := monitor.Snapshot(time.Now())

	if got.Chats != 0 || got.Waiting != 0 || got.Message != "" || got.Focus != nil {
		t.Errorf("Snapshot() = %+v, want zero state", got)
	}

	if got.Plan.FiveHour.Pct != domain.UnknownPct {
		t.Errorf("Plan without a source should be unknown, got %+v", got.Plan)
	}
}
