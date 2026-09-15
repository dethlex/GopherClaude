package usecase

import (
	"io"
	"log/slog"
	"strconv"
	"strings"
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
		{ID: "s3", Dir: "/Users/x/gamma", PID: 4242, Name: "gamma-7f"},
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

	if got.Message != "gamma-7f PERM" {
		t.Errorf("Message = %q, want %q (most recent waiting session, by its name)", got.Message, "gamma-7f PERM")
	}

	if got.Usage.Input != 10 || got.Usage.Output != 20 {
		t.Errorf("Usage = %+v", got.Usage)
	}

	if len(got.Sessions) != 3 {
		t.Fatalf("Sessions = %d rows, want 3", len(got.Sessions))
	}

	// Permission waits sort first, then input waits, then working.
	if got.Sessions[0].Name != "gamma-7f" || got.Sessions[0].Phase != domain.PhaseWaitingPermission {
		t.Errorf("Sessions[0] = %+v, want gamma-7f PERM", got.Sessions[0])
	}

	if got.Sessions[1].Name != "beta" || got.Sessions[2].Name != "alpha" {
		t.Errorf("Sessions order = %s, %s; want beta, alpha", got.Sessions[1].Name, got.Sessions[2].Name)
	}

	// gamma waits since base+1m, snapshot at base+2m => 1 minute.
	if got.Sessions[0].Minutes != 1 {
		t.Errorf("Sessions[0].Minutes = %d, want 1", got.Sessions[0].Minutes)
	}

	if got.Focus == nil || got.Focus.Dir != "/Users/x/gamma" || got.Focus.PID != 4242 || got.Focus.SessionID != "s3" {
		t.Errorf("Focus = %+v, want gamma/4242/s3", got.Focus)
	}

	if len(got.FocusTargets) != len(got.Sessions) || got.FocusTargets[0].PID != 4242 || got.FocusTargets[0].SessionID != "s3" {
		t.Errorf("FocusTargets = %+v, want the session id carried along", got.FocusTargets)
	}

	// Without other assistants wired there is nothing to list: the badge
	// then offers the Claude screen alone.
	if len(got.Extras) != 0 {
		t.Errorf("Extras = %+v, want none", got.Extras)
	}
}

func TestMonitorMergesExtras(t *testing.T) {
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	claudeSess := []domain.Session{{ID: "c1", Dir: "/Users/x/claude-proj", PID: 1}}
	claudeStates := []domain.SessionState{
		{Session: claudeSess[0], Phase: domain.PhaseWaitingInput, Reason: "INPUT", Since: base},
	}

	agySess := []domain.Session{
		{Provider: domain.ProviderAntigravity, ID: "a1", Dir: "/Users/x/agy-proj", PID: 2},
		{Provider: domain.ProviderAntigravity, ID: "a2", Dir: "/Users/x/agy-other", PID: 3},
	}
	agyStates := []domain.SessionState{
		{Session: agySess[0], Phase: domain.PhaseWaitingInput, Reason: "INPUT", Since: base.Add(5 * time.Minute)},
		{Session: agySess[1], Phase: domain.PhaseWorking},
	}

	codexSess := []domain.Session{
		{Provider: domain.ProviderCodex, ID: "x1", Dir: "/Users/x/codex-proj", PID: 4},
	}
	codexStates := []domain.SessionState{
		{Session: codexSess[0], Phase: domain.PhaseWaitingPermission, Reason: "PERM", Since: base.Add(7 * time.Minute)},
	}

	monitor := NewMonitor(Sources{
		Claude: ProviderSources{Sessions: fakeSessions{claudeSess}, Phases: fakePhases{claudeStates}},
		Usage:  fakeUsage{},
		Extras: []ExtraSources{
			{Provider: domain.ProviderAntigravity, ProviderSources: ProviderSources{
				Sessions: fakeSessions{agySess},
				Phases:   fakePhases{agyStates},
				Plan:     fakePlan{domain.PlanUsage{FiveHour: domain.Limit{Pct: 22}, Weekly: domain.Limit{Pct: 18}}},
				Prompts:  fakePrompts{7},
			}},
			{Provider: domain.ProviderCodex, ProviderSources: ProviderSources{
				Sessions: fakeSessions{codexSess},
				Phases:   fakePhases{codexStates},
				Plan:     fakePlan{domain.PlanUsage{FiveHour: domain.Limit{Pct: domain.UnknownPct}, Weekly: domain.Limit{Pct: 17}}},
				Prompts:  fakePrompts{3},
			}},
		},
	}, testLogger())

	got := monitor.Snapshot(base.Add(10 * time.Minute))

	if got.Chats != 1 || got.Waiting != 1 {
		t.Errorf("Claude Chats/Waiting = %d/%d, want 1/1", got.Chats, got.Waiting)
	}

	if len(got.Extras) != 2 {
		t.Fatalf("Extras = %d blocks, want 2", len(got.Extras))
	}

	agy, codex := got.Extras[0], got.Extras[1]

	if agy.Provider != domain.ProviderAntigravity || agy.Chats != 2 || agy.Waiting != 1 || agy.Prompts != 7 || agy.Plan.FiveHour.Pct != 22 {
		t.Errorf("Extras[0] = %+v, want the Antigravity block", agy)
	}

	if codex.Provider != domain.ProviderCodex || codex.Chats != 1 || codex.Waiting != 1 || codex.Prompts != 3 || codex.Plan.Weekly.Pct != 17 {
		t.Errorf("Extras[1] = %+v, want the Codex block", codex)
	}

	// The banner follows the newest wait regardless of provider.
	if got.Message != "codex-proj PERM" || got.Focus == nil || got.Focus.PID != 4 {
		t.Errorf("Message=%q Focus=%+v, want the codex session", got.Message, got.Focus)
	}

	if len(got.Sessions) != 4 {
		t.Fatalf("Sessions = %d, want 4 merged rows", len(got.Sessions))
	}

	// Permission waits first, then input waits (older on top), working last.
	wantOrder := []string{"codex-proj", "claude-proj", "agy-proj", "agy-other"}
	for i, name := range wantOrder {
		if got.Sessions[i].Name != name {
			t.Errorf("Sessions[%d] = %q, want %q", i, got.Sessions[i].Name, name)
		}
	}

	if got.Sessions[0].Provider != domain.ProviderCodex || got.FocusTargets[0].PID != 4 {
		t.Errorf("Sessions[0]/FocusTargets[0] = %+v / %+v", got.Sessions[0], got.FocusTargets[0])
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

// The cap is the badge's: it lists 7 rows per page and its line buffer is
// sized for 32 entries. Permission waits sort first, so the cut never drops
// one.
func TestSessionListCapsAtBadgeRows(t *testing.T) {
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	states := make([]domain.SessionState, 0, 41)
	for i := 0; i < 40; i++ {
		states = append(states, domain.SessionState{
			Session: domain.Session{ID: "w" + strconv.Itoa(i), Dir: "/Users/x/work" + strconv.Itoa(i)},
			Phase:   domain.PhaseWorking,
		})
	}

	states = append(states, domain.SessionState{
		Session: domain.Session{ID: "perm", PID: 7, Dir: "/Users/x/perm"},
		Phase:   domain.PhaseWaitingPermission,
		Reason:  "PERM",
		Since:   base,
	})

	briefs, targets := sessionList(states, base)

	if domain.MaxSessionRows != 32 {
		t.Errorf("MaxSessionRows = %d, want 32 (firmware maxSessions)", domain.MaxSessionRows)
	}

	if len(briefs) != domain.MaxSessionRows || len(targets) != domain.MaxSessionRows {
		t.Fatalf("rows = %d briefs / %d targets, want %d", len(briefs), len(targets), domain.MaxSessionRows)
	}

	if briefs[0].Name != "perm" || targets[0].PID != 7 {
		t.Errorf("first row = %+v / %+v, want the permission wait", briefs[0], targets[0])
	}
}

func TestSessionListNamesTitlesAndPaths(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	states := []domain.SessionState{
		// Claude: the registry name labels the row and doubles as the title.
		{Session: domain.Session{Dir: "/Users/x/go/src/t10s", Name: "fix-prime-minbid"}, Phase: domain.PhaseWorking},
		// Codex: no handle, so the project labels the row; the first prompt,
		// its line breaks and runs of spaces folded, is the title.
		{Session: domain.Session{Provider: domain.ProviderCodex, Dir: "/Users/x/api", Title: "  add   retries\nto the client "}, Phase: domain.PhaseWorking},
		// Nothing known: project label, no title.
		{Session: domain.Session{Provider: domain.ProviderAntigravity, Dir: "/Users/x/rotator"}, Phase: domain.PhaseWorking},
	}

	briefs, _ := sessionList(states, now)

	want := []domain.SessionBrief{
		{Name: "fix-prime-minbid", Title: "fix-prime-minbid", Path: "/Users/x/go/src/t10s"},
		{Name: "api", Title: "add retries to the client", Path: "/Users/x/api"},
		{Name: "rotator", Title: "", Path: "/Users/x/rotator"},
	}

	if len(briefs) != len(want) {
		t.Fatalf("rows = %d, want %d", len(briefs), len(want))
	}

	for i := range want {
		got := briefs[i]
		if got.Name != want[i].Name || got.Title != want[i].Title || got.Path != want[i].Path {
			t.Errorf("row %d = {Name:%q Title:%q Path:%q}, want {Name:%q Title:%q Path:%q}",
				i, got.Name, got.Title, got.Path, want[i].Name, want[i].Title, want[i].Path)
		}
	}
}

func TestFoldSpace(t *testing.T) {
	if got := foldSpace(" a \n\n b\tc  "); got != "a b c" {
		t.Errorf("foldSpace = %q, want %q", got, "a b c")
	}

	if got := foldSpace(strings.Repeat(" ", 3)); got != "" {
		t.Errorf("foldSpace(blank) = %q, want empty", got)
	}
}

// A blank registry name is no name: the row falls back to the project so
// the badge never receives an empty label (it would drop the row and shift
// every later focus index).
func TestRowLabelFallsBackFromBlankName(t *testing.T) {
	if got := rowLabel(domain.Session{Dir: "/Users/x/proj", Name: " \t "}); got != "proj" {
		t.Errorf("rowLabel(blank name) = %q, want %q", got, "proj")
	}
}
