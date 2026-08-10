package main

import (
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func TestDemoSceneCycles(t *testing.T) {
	seen := make(map[int]string, len(demoScenes))

	for i := range demoScenes {
		elapsed := time.Duration(i)*demoStep + demoStep/2

		name, _, idx := demoScene(elapsed)
		if idx != i {
			t.Errorf("at %v index = %d, want %d", elapsed, idx, i)
		}

		seen[idx] = name
	}

	if len(seen) != len(demoScenes) {
		t.Errorf("visited %d scenes, want %d", len(seen), len(demoScenes))
	}

	// The cycle must wrap back to the first scene.
	if _, _, idx := demoScene(time.Duration(len(demoScenes))*demoStep + demoStep/2); idx != 0 {
		t.Errorf("after a full cycle index = %d, want 0", idx)
	}
}

// Every scene must reach the firmware as a distinct eye state, so the phases
// and counts have to line up with what the badge derives from them.
func TestDemoScenesCoverEyeStates(t *testing.T) {
	states := map[string]bool{}

	for i := range demoScenes {
		_, snap, _ := demoScene(time.Duration(i)*demoStep + demoStep/2)

		perm, input, working := 0, 0, 0

		for _, s := range snap.Sessions {
			switch s.Phase {
			case domain.PhaseWaitingPermission:
				perm++
			case domain.PhaseWaitingInput:
				input++
			case domain.PhaseWorking:
				working++
			}
		}

		switch {
		case perm > 0:
			states["perm"] = true
		case snap.Waiting > 0:
			states["input"] = true
		case working > 0:
			states["working"] = true
		default:
			states["quiet"] = true
		}

		// The badge derives "working" as chats-waiting, so the synthetic
		// counts must stay consistent with the row phases.
		if snap.Chats-snap.Waiting != working {
			t.Errorf("scene %d: chats-waiting = %d but %d working rows",
				i, snap.Chats-snap.Waiting, working)
		}

		if snap.Plan.FiveHour.Pct != domain.UnknownPct {
			t.Errorf("scene %d: plan bars should be blank in demo", i)
		}
	}

	for _, want := range []string{"quiet", "working", "input", "perm"} {
		if !states[want] {
			t.Errorf("no demo scene produces the %q eye state", want)
		}
	}
}
