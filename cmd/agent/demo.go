package main

import (
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// demoStep is how long each synthetic state is held. Long enough to watch the
// eyes settle after the ~8s alert window closes.
const demoStep = 12 * time.Second

// demoScenes cycles through the states the eyes distinguish, so the patterns
// can be compared side by side instead of waiting for the real thing (a
// permission prompt never happens under --dangerously-skip-permissions, for
// example).
var demoScenes = []struct {
	name     string
	snapshot domain.Snapshot
}{
	{
		// Green means nothing is going on at all: the badge derives
		// "working" as chats-waiting, so any live session that is not
		// waiting counts as work in progress.
		name:     "QUIET: no sessions, steady green",
		snapshot: domain.Snapshot{Chats: 0, Waiting: 0},
	},
	{
		name: "WORKING: alternating cyan",
		snapshot: domain.Snapshot{
			Chats: 2, Waiting: 0,
			Message: "DEMO working",
			Sessions: []domain.SessionBrief{
				{Name: "demo-a", Phase: domain.PhaseWorking, CtxTokens: 120_000},
				{Name: "demo-b", Phase: domain.PhaseWorking, CtxTokens: 80_000},
			},
		},
	},
	{
		name: "WAITING INPUT: synced amber blink, then steady",
		snapshot: domain.Snapshot{
			Chats: 2, Waiting: 1,
			Message: "DEMO input",
			Sessions: []domain.SessionBrief{
				{Name: "demo-a", Phase: domain.PhaseWaitingInput, Minutes: 3},
				{Name: "demo-b", Phase: domain.PhaseWorking},
			},
		},
	},
	{
		name: "NEEDS PERMISSION: alternating red",
		snapshot: domain.Snapshot{
			Chats: 2, Waiting: 1,
			Message: "DEMO perm",
			Sessions: []domain.SessionBrief{
				{Name: "demo-a", Phase: domain.PhaseWaitingPermission, Minutes: 1},
				{Name: "demo-b", Phase: domain.PhaseWorking},
			},
		},
	},
}

// demoScene picks the scene for the given elapsed time and reports whether it
// differs from the previous one (so the caller can log each transition once).
func demoScene(elapsed time.Duration) (name string, snapshot domain.Snapshot, index int) {
	idx := int(elapsed/demoStep) % len(demoScenes)
	scene := demoScenes[idx]

	// Quiet must really look quiet: no leftover banner text.
	return scene.name, withUnknownPlan(scene.snapshot), idx
}

// withUnknownPlan blanks the plan bars: the demo says nothing about limits, and
// stale-looking bars would be misleading.
func withUnknownPlan(s domain.Snapshot) domain.Snapshot {
	s.Plan = domain.UnknownPlanUsage()

	return s
}
