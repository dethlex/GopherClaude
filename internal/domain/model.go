// Package domain holds the core types and source interfaces of the badge
// agent. It must not import any other internal package.
package domain

import "time"

// Phase describes what a Claude Code session is doing right now.
type Phase int

const (
	// PhaseWorking — the model is generating or a tool is running.
	PhaseWorking Phase = iota
	// PhaseWaitingInput — the turn ended, Claude waits for the user's prompt.
	PhaseWaitingInput
	// PhaseWaitingPermission — a permission dialog is open.
	PhaseWaitingPermission
)

// Waiting reports whether the phase requires a user action.
func (p Phase) Waiting() bool {
	return p == PhaseWaitingInput || p == PhaseWaitingPermission
}

type (
	// Session is one live interactive Claude Code session on this machine.
	Session struct {
		ID        string
		PID       int
		Dir       string
		Status    string
		StartedAt time.Time
	}

	// SessionState is a Session with its resolved phase.
	SessionState struct {
		Session Session
		Phase   Phase
		Reason  string
		Since   time.Time
		// CtxTokens approximates the session's current context size:
		// the token accounting of its latest assistant response.
		CtxTokens uint64
	}

	// SessionBrief is one row of the badge's session list page.
	SessionBrief struct {
		Name      string
		Phase     Phase
		Minutes   int // time spent in the current phase, 0 when unknown
		CtxTokens uint64
	}

	// Usage is the aggregated token usage for the current day.
	Usage struct {
		Input         uint64
		Output        uint64
		CacheCreation uint64
		CacheRead     uint64
	}

	// Limit is one plan rate-limit bucket (5-hour, weekly).
	Limit struct {
		Pct      int // 0..100, UnknownPct when unavailable
		ResetsAt time.Time
	}

	// PlanUsage mirrors the "Plan usage" panel of Claude Desktop.
	PlanUsage struct {
		FiveHour    Limit
		Weekly      Limit
		CreditsPct  int    // extra usage credits, UnknownPct when disabled
		CreditsText string // e.g. "32.66/50"
		// FiveHourETA predicts when the 5-hour limit hits 100% at the
		// current burn rate. Zero when unknown or when the limit resets
		// before it would be exhausted (i.e. the forecast is not the
		// binding constraint).
		FiveHourETA time.Duration
	}

	// FocusTarget identifies the session a "jump to chat" button should
	// bring to the foreground on the host.
	FocusTarget struct {
		PID int
		Dir string
	}

	// Snapshot is what the badge ultimately renders.
	Snapshot struct {
		Chats    int
		Waiting  int
		Usage    Usage
		Plan     PlanUsage
		Message  string
		Sessions []SessionBrief
		// Focus is the session named in the banner — the dashboard jump
		// button's target. Nil when nothing is waiting.
		Focus *FocusTarget
		// FocusTargets is one target per Sessions row, in the same order,
		// so the badge can ask to open a specific list row by index.
		FocusTargets []FocusTarget
	}

	// Command is a button action the badge sends back to the host.
	Command struct {
		Name  string
		Index int // session-list row to act on, or NoIndex
	}
)

const (
	// CommandFocus asks the host to foreground a session.
	CommandFocus = "focus"

	// NoIndex marks a command that carries no session-row index (act on
	// the banner session instead).
	NoIndex = -1
)

// UnknownPct marks a limit whose value could not be obtained.
const UnknownPct = -1

// UnknownPlanUsage is a PlanUsage with every value marked unavailable.
func UnknownPlanUsage() PlanUsage {
	return PlanUsage{
		FiveHour:   Limit{Pct: UnknownPct},
		Weekly:     Limit{Pct: UnknownPct},
		CreditsPct: UnknownPct,
	}
}

type (
	// SessionSource lists live interactive sessions.
	SessionSource interface {
		Sessions() ([]Session, error)
	}

	// UsageSource aggregates today's token usage.
	UsageSource interface {
		TodayUsage(now time.Time) (Usage, error)
	}

	// PlanSource reports plan limit utilization. Implementations degrade to
	// UnknownPlanUsage instead of failing the snapshot.
	PlanSource interface {
		Plan(now time.Time) PlanUsage
	}

	// PhaseResolver determines what each session is doing.
	PhaseResolver interface {
		ResolveAll(sessions []Session, now time.Time) []SessionState
	}

	// Sink delivers snapshots to the badge (or to a log in dry-run mode)
	// and returns any button commands the badge sent back.
	Sink interface {
		Send(snapshot Snapshot) ([]Command, error)
		Close() error
	}

	// Focuser brings a session's window to the foreground on the host.
	Focuser interface {
		Focus(target FocusTarget) error
	}
)
