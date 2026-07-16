// Package usecase combines the data sources into badge snapshots.
package usecase

import (
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// maxSessionRows caps the session list sent to the badge; its list page
	// cannot show more anyway.
	maxSessionRows = 8

	maxPhaseMinutes = 999
)

// Monitor builds a Snapshot of all live Claude Code sessions.
type Monitor struct {
	sessions domain.SessionSource
	usage    domain.UsageSource
	plan     domain.PlanSource
	phases   domain.PhaseResolver
	logger   *slog.Logger
}

func NewMonitor(
	sessions domain.SessionSource,
	usage domain.UsageSource,
	plan domain.PlanSource,
	phases domain.PhaseResolver,
	logger *slog.Logger,
) *Monitor {
	return &Monitor{
		sessions: sessions,
		usage:    usage,
		plan:     plan,
		phases:   phases,
		logger:   logger.With("module", "monitor"),
	}
}

// Snapshot collects the current state. Source failures degrade the snapshot
// (zero values) instead of aborting it: a half-filled badge beats a dead one.
func (m *Monitor) Snapshot(now time.Time) domain.Snapshot {
	sessions, err := m.sessions.Sessions()
	if err != nil {
		m.logger.Error("list sessions", "error", err)
	}

	usage, err := m.usage.TodayUsage(now)
	if err != nil {
		m.logger.Error("aggregate usage", "error", err)
	}

	states := m.phases.ResolveAll(sessions, now)

	waiting := 0

	var newest *domain.SessionState

	for i := range states {
		if !states[i].Phase.Waiting() {
			continue
		}

		waiting++

		if newest == nil || states[i].Since.After(newest.Since) {
			newest = &states[i]
		}
	}

	msg := ""

	var focus *domain.FocusTarget

	if newest != nil {
		msg = filepath.Base(newest.Session.Dir) + " " + newest.Reason
		focus = &domain.FocusTarget{
			PID: newest.Session.PID,
			Dir: newest.Session.Dir,
		}
	}

	briefs, targets := sessionList(states, now)

	return domain.Snapshot{
		Chats:        len(sessions),
		Waiting:      waiting,
		Usage:        usage,
		Plan:         m.plan.Plan(now),
		Message:      msg,
		Sessions:     briefs,
		Focus:        focus,
		FocusTargets: targets,
	}
}

// sessionList builds the badge's session page: permission waits first, then
// input waits, then working sessions; longer waits on top. Capped at
// maxSessionRows. The returned slices share one order, so a row index from
// the badge selects the matching focus target.
func sessionList(states []domain.SessionState, now time.Time) ([]domain.SessionBrief, []domain.FocusTarget) {
	ordered := make([]domain.SessionState, len(states))
	copy(ordered, states)

	sort.SliceStable(ordered, func(i, j int) bool {
		if pi, pj := phaseRank(ordered[i].Phase), phaseRank(ordered[j].Phase); pi != pj {
			return pi < pj
		}

		return ordered[i].Since.Before(ordered[j].Since)
	})

	if len(ordered) > maxSessionRows {
		ordered = ordered[:maxSessionRows]
	}

	briefs := make([]domain.SessionBrief, 0, len(ordered))
	targets := make([]domain.FocusTarget, 0, len(ordered))

	for _, st := range ordered {
		briefs = append(briefs, domain.SessionBrief{
			Name:      filepath.Base(st.Session.Dir),
			Phase:     st.Phase,
			Minutes:   phaseMinutes(st.Since, now),
			CtxTokens: st.CtxTokens,
		})
		targets = append(targets, domain.FocusTarget{
			PID: st.Session.PID,
			Dir: st.Session.Dir,
		})
	}

	return briefs, targets
}

func phaseRank(p domain.Phase) int {
	switch p {
	case domain.PhaseWaitingPermission:
		return 0
	case domain.PhaseWaitingInput:
		return 1
	case domain.PhaseWorking:
		return 2
	default:
		return 3
	}
}

// phaseMinutes is how long the session has been in its phase; 0 both for
// "just now" and for heuristic states whose start time is unknown.
func phaseMinutes(since, now time.Time) int {
	if since.IsZero() || since.After(now) {
		return 0
	}

	minutes := int(now.Sub(since).Minutes())
	if minutes > maxPhaseMinutes {
		return maxPhaseMinutes
	}

	return minutes
}
