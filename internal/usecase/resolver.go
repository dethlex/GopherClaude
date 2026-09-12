package usecase

import (
	"log/slog"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Reason labels shown on the badge banner next to the project name.
const (
	reasonPermission = "PERM"
	reasonInput      = "INPUT"
)

// workingEventTTL bounds how long a "working" hook event stays authoritative.
// Hooks are lossy: claude-desktop sessions never emit Stop, so the last event
// of a finished turn is a PostToolUse — and without an expiry that would pin
// the session to "working" forever (a spinner that never stops). "Waiting"
// events get no expiry on purpose: a permission dialog can sit for hours and
// the transcript cannot tell one from a running tool, while "working" is
// momentary and the inspector detects it reliably on its own.
const workingEventTTL = 30 * time.Second

// Resolver determines each session's phase, combining sources by precedence:
// hook events (exact) > the provider's own idle flag > the provider's
// inspector heuristic. It is provider-neutral: every assistant plugs in its
// event source and inspector.
type Resolver struct {
	events    domain.HookEventSource
	inspector domain.PhaseInspector
	logger    *slog.Logger
}

var _ domain.PhaseResolver = (*Resolver)(nil)

func NewResolver(events domain.HookEventSource, inspector domain.PhaseInspector, logger *slog.Logger) *Resolver {
	return &Resolver{
		events:    events,
		inspector: inspector,
		logger:    logger.With("module", "resolver"),
	}
}

func (r *Resolver) ResolveAll(sessions []domain.Session, now time.Time) []domain.SessionState {
	latest, err := r.events.Latest()
	if err != nil {
		r.logger.Warn("read hook events", "error", err)
	}

	states := make([]domain.SessionState, 0, len(sessions))
	for _, session := range sessions {
		states = append(states, r.resolve(session, latest, now))
	}

	return states
}

func (r *Resolver) resolve(session domain.Session, events map[string]domain.HookEvent, now time.Time) domain.SessionState {
	// The inspector runs regardless of how the phase is decided: it is the
	// only source of the session's context size.
	tailPhase, ctxTokens, err := r.inspector.Inspect(session)
	if err != nil {
		r.logger.Debug("phase heuristic", "session", session.ID, "error", err)
	}

	if event, ok := events[session.ID]; ok && event.Decisive && eventAuthoritative(event.Phase, event.At, now) {
		return domain.SessionState{
			Session:   session,
			Phase:     event.Phase,
			Reason:    reasonFor(event.Phase),
			Since:     event.At,
			CtxTokens: ctxTokens,
		}
	}

	phase := tailPhase
	if session.Idle {
		phase = domain.PhaseWaitingInput
	}

	return domain.SessionState{
		Session:   session,
		Phase:     phase,
		Reason:    reasonFor(phase),
		Since:     now,
		CtxTokens: ctxTokens,
	}
}

// eventAuthoritative reports whether a hook event may still decide the phase:
// waiting events are durable, working ones expire (see workingEventTTL).
func eventAuthoritative(phase domain.Phase, at, now time.Time) bool {
	if phase.Waiting() {
		return true
	}

	return !now.After(at.Add(workingEventTTL))
}

func reasonFor(phase domain.Phase) string {
	switch phase {
	case domain.PhaseWaitingPermission:
		return reasonPermission
	case domain.PhaseWaitingInput:
		return reasonInput
	case domain.PhaseWorking:
		return ""
	default:
		return ""
	}
}
