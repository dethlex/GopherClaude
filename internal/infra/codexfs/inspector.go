package codexfs

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// RolloutDir is the phase heuristic for Codex threads that have no hook
// events: the rollout's newest task event says whether a turn is running
// (task_started) or over (task_complete, turn_aborted), and its newest
// token_count carries the context size. Approval prompts are not written to
// the rollout, so only hooks can see "waiting for permission".
type RolloutDir struct {
	sessionsDir string

	// A thread's rollout path never changes; remembered so Inspect costs
	// one tail read, not a glob.
	paths map[string]string
}

func NewRolloutDir(sessionsDir string) *RolloutDir {
	return &RolloutDir{sessionsDir: sessionsDir, paths: map[string]string{}}
}

// Inspect implements the phase-inspector contract used by the resolver.
func (d *RolloutDir) Inspect(session domain.Session) (domain.Phase, uint64, error) {
	path, err := d.path(session.ID)
	if err != nil {
		// No transcript yet: the thread has not run a turn, so nothing is
		// in flight.
		return domain.PhaseWaitingInput, 0, nil
	}

	tail, err := readTail(path, tailReadSize)
	if err != nil {
		return domain.PhaseWaitingInput, 0, fmt.Errorf("read rollout %q: %w", path, err)
	}

	phase, ctx := inspectTail(tail)

	return phase, ctx, nil
}

func (d *RolloutDir) path(threadID string) (string, error) {
	if p, ok := d.paths[threadID]; ok {
		return p, nil
	}

	p, err := findRollout(d.sessionsDir, threadID)
	if err != nil {
		return "", err
	}

	d.paths[threadID] = p

	return p, nil
}

// inspectTail walks the tail bottom-up: the newest task event decides the
// phase, the newest token_count the context size. No task event at all (a
// thread that never ran) reads as waiting for input.
func inspectTail(tail []byte) (domain.Phase, uint64) {
	var (
		phase    = domain.PhaseWaitingInput
		phaseSet bool
		ctx      uint64
	)

	lines := bytes.Split(tail, []byte{'\n'})

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if len(line) == 0 {
			continue
		}

		var rec record
		if err := json.Unmarshal(line, &rec); err != nil || rec.Type != recEventMsg {
			continue // the first chunk after the seek may be a partial line
		}

		var ev eventPayload
		if err := json.Unmarshal(rec.Payload, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case evTaskStarted:
			if !phaseSet {
				phase, phaseSet = domain.PhaseWorking, true
			}
		case evTaskComplete, evTurnAborted:
			phaseSet = true
		case evTokenCount:
			if ctx == 0 {
				ctx = ev.Info.Last.Total
			}
		}

		if phaseSet && ctx > 0 {
			break
		}
	}

	return phase, ctx
}
