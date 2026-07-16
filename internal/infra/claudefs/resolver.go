package claudefs

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

const statusIdle = "idle"

// Resolver determines each session's phase, combining sources by precedence:
// hook events (exact) > registry status (cli sessions only) > transcript
// tail heuristic.
type Resolver struct {
	events      *EventLog
	transcripts *TranscriptDir
	logger      *slog.Logger
}

func NewResolver(events *EventLog, transcripts *TranscriptDir, logger *slog.Logger) *Resolver {
	return &Resolver{
		events:      events,
		transcripts: transcripts,
		logger:      logger.With("module", "resolver"),
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

func (r *Resolver) resolve(session domain.Session, events map[string]Event, now time.Time) domain.SessionState {
	// The transcript tail is read regardless of how the phase is decided:
	// it is the only source of the session's context size.
	tailPhase, ctxTokens, err := r.transcripts.Inspect(session)
	if err != nil {
		r.logger.Debug("transcript heuristic", "session", session.ID, "error", err)
	}

	if event, ok := events[session.ID]; ok {
		if phase, decisive := event.Phase(); decisive {
			return domain.SessionState{
				Session:   session,
				Phase:     phase,
				Reason:    reasonFor(phase),
				Since:     event.At,
				CtxTokens: ctxTokens,
			}
		}
	}

	phase := tailPhase
	if session.Status == statusIdle {
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
