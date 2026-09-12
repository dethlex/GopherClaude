package usecase

import (
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

type (
	fakeEvents    map[string]domain.HookEvent
	fakeInspector struct {
		phase domain.Phase
		ctx   uint64
	}
)

func (f fakeEvents) Latest() (map[string]domain.HookEvent, error) { return f, nil }

func (f fakeInspector) Inspect(domain.Session) (domain.Phase, uint64, error) {
	return f.phase, f.ctx, nil
}

const resolverSessionID = "sess-resolve"

func resolveOne(t *testing.T, events fakeEvents, inspector fakeInspector, idle bool, now time.Time) domain.SessionState {
	t.Helper()

	r := NewResolver(events, inspector, testLogger())

	states := r.ResolveAll([]domain.Session{{ID: resolverSessionID, Dir: "/Users/x/proj", Idle: idle}}, now)
	if len(states) != 1 {
		t.Fatalf("ResolveAll returned %d states, want 1", len(states))
	}

	return states[0]
}

func hookEvent(at time.Time, phase domain.Phase) fakeEvents {
	return fakeEvents{resolverSessionID: {SessionID: resolverSessionID, Phase: phase, Decisive: true, At: at}}
}

// A stale working event must not pin a finished session to "working": Claude
// Desktop sessions never emit Stop, so without an expiry the badge spinner
// would spin forever.
func TestResolverStaleWorkingEventFallsBackToInspector(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)

	got := resolveOne(t, hookEvent(now.Add(-5*time.Minute), domain.PhaseWorking),
		fakeInspector{phase: domain.PhaseWaitingInput, ctx: 4200}, false, now)

	if got.Phase != domain.PhaseWaitingInput || got.Reason != "INPUT" {
		t.Errorf("state = %+v, want waiting for input (the inspector says the turn ended)", got)
	}

	if got.CtxTokens != 4200 {
		t.Errorf("CtxTokens = %d, want the inspector's 4200", got.CtxTokens)
	}
}

// A fresh working event still wins: it is the fastest signal that work began.
func TestResolverFreshWorkingEventWins(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)

	got := resolveOne(t, hookEvent(now.Add(-2*time.Second), domain.PhaseWorking),
		fakeInspector{phase: domain.PhaseWaitingInput}, false, now)

	if got.Phase != domain.PhaseWorking || got.Reason != "" {
		t.Errorf("state = %+v, want working", got)
	}
}

// Waiting events never expire: a permission dialog can sit for hours, and the
// transcript cannot tell one from a running tool.
func TestResolverOldPermissionEventStillWins(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)

	got := resolveOne(t, hookEvent(since, domain.PhaseWaitingPermission),
		fakeInspector{phase: domain.PhaseWorking, ctx: 77}, false, now)

	if got.Phase != domain.PhaseWaitingPermission || got.Reason != "PERM" || !got.Since.Equal(since) || got.CtxTokens != 77 {
		t.Errorf("state = %+v, want permission wait since %v with the inspector's context", got, since)
	}
}

// An event that decides nothing (an unknown notification kind) leaves the
// decision to the inspector.
func TestResolverIndecisiveEventIsIgnored(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	events := fakeEvents{resolverSessionID: {SessionID: resolverSessionID, Phase: domain.PhaseWorking, Decisive: false, At: now}}

	got := resolveOne(t, events, fakeInspector{phase: domain.PhaseWaitingInput}, false, now)

	if got.Phase != domain.PhaseWaitingInput {
		t.Errorf("Phase = %v, want the inspector's waiting for input", got.Phase)
	}
}

// A registry that marks the session idle overrides a busy-looking transcript.
func TestResolverIdleSessionWaits(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)

	got := resolveOne(t, fakeEvents{}, fakeInspector{phase: domain.PhaseWorking}, true, now)

	if got.Phase != domain.PhaseWaitingInput || !got.Since.Equal(now) {
		t.Errorf("state = %+v, want waiting for input since now", got)
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
