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

// ProviderSources are the feeds for one assistant. Plan and Prompts may be nil.
type ProviderSources struct {
	Sessions domain.SessionSource
	Phases   domain.PhaseResolver
	Plan     domain.PlanSource
	Prompts  domain.PromptSource
}

// ExtraSources are the feeds of an installed assistant other than Claude.
type ExtraSources struct {
	Provider domain.Provider
	ProviderSources
}

// Sources wires the monitor. Claude is mandatory; Extras lists the other
// installed assistants in display order (empty = Claude alone).
type Sources struct {
	Claude ProviderSources
	Usage  domain.UsageSource
	Extras []ExtraSources
}

// Monitor builds a Snapshot of all live assistant sessions.
type Monitor struct {
	src    Sources
	logger *slog.Logger
}

func NewMonitor(src Sources, logger *slog.Logger) *Monitor {
	return &Monitor{src: src, logger: logger.With("module", "monitor")}
}

// Snapshot collects the current state. Source failures degrade the snapshot
// (zero values) instead of aborting it: a half-filled badge beats a dead one.
func (m *Monitor) Snapshot(now time.Time) domain.Snapshot {
	claude := m.collect(m.src.Claude, now)

	usage, err := m.src.Usage.TodayUsage(now)
	if err != nil {
		m.logger.Error("aggregate usage", "error", err)
	}

	snap := domain.Snapshot{
		Chats:   len(claude.states),
		Waiting: claude.waiting,
		Usage:   usage,
		Plan:    planOf(m.src.Claude.Plan, now),
	}

	all := claude.states

	for _, extra := range m.src.Extras {
		got := m.collect(extra.ProviderSources, now)
		all = append(all, got.states...)

		snap.Extras = append(snap.Extras, domain.ProviderStats{
			Provider: extra.Provider,
			Chats:    len(got.states),
			Waiting:  got.waiting,
			Plan:     planOf(extra.Plan, now),
			Prompts:  promptsOf(extra.Prompts, now),
		})
	}

	if newest := newestWaiting(all); newest != nil {
		snap.Message = filepath.Base(newest.Session.Dir) + " " + newest.Reason
		snap.Focus = &domain.FocusTarget{PID: newest.Session.PID, Dir: newest.Session.Dir}
	}

	snap.Sessions, snap.FocusTargets = sessionList(all, now)

	return snap
}

type collected struct {
	states  []domain.SessionState
	waiting int
}

func (m *Monitor) collect(src ProviderSources, now time.Time) collected {
	sessions, err := src.Sessions.Sessions()
	if err != nil {
		m.logger.Error("list sessions", "error", err)
	}

	states := src.Phases.ResolveAll(sessions, now)

	waiting := 0

	for i := range states {
		if states[i].Phase.Waiting() {
			waiting++
		}
	}

	return collected{states: states, waiting: waiting}
}

func planOf(src domain.PlanSource, now time.Time) domain.PlanUsage {
	if src == nil {
		return domain.UnknownPlanUsage()
	}

	return src.Plan(now)
}

func promptsOf(src domain.PromptSource, now time.Time) int {
	if src == nil {
		return 0
	}

	return src.PromptsToday(now)
}

// newestWaiting is the banner session: the most recent wait across providers.
func newestWaiting(states []domain.SessionState) *domain.SessionState {
	var newest *domain.SessionState

	for i := range states {
		if !states[i].Phase.Waiting() {
			continue
		}

		if newest == nil || states[i].Since.After(newest.Since) {
			newest = &states[i]
		}
	}

	return newest
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
			Provider:  st.Session.Provider,
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
