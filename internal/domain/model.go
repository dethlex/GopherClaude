// Package domain holds the core types and source interfaces of the badge
// agent. It must not import any other internal package.
package domain

import "time"

// Provider is the coding assistant a session belongs to.
type Provider int

const (
	// ProviderClaude — Claude Code (cli or Claude Desktop).
	ProviderClaude Provider = iota
	// ProviderAntigravity — Antigravity CLI (agy, Gemini-backed).
	ProviderAntigravity
	// ProviderCodex — OpenAI Codex (cli, `codex exec` and Codex Desktop threads).
	ProviderCodex
)

// Phase describes what a session is doing right now.
type Phase int

const (
	// PhaseWorking — the model is generating or a tool is running.
	PhaseWorking Phase = iota
	// PhaseWaitingInput — the turn ended, the assistant waits for a prompt.
	PhaseWaitingInput
	// PhaseWaitingPermission — a permission dialog is open.
	PhaseWaitingPermission
)

// Waiting reports whether the phase requires a user action.
func (p Phase) Waiting() bool {
	return p == PhaseWaitingInput || p == PhaseWaitingPermission
}

// MaxSessionRows caps the session list sent to the badge: its list page
// shows 7 rows and its line buffer is sized for 32 entries of the longest
// allowed text.
const MaxSessionRows = 32

type (
	// Session is one live interactive assistant session on this machine.
	Session struct {
		Provider Provider
		ID       string
		PID      int
		Dir      string
		// Name is the provider's own short handle for the session (Claude
		// Code names every session, derived or via /rename); it fits a
		// list row. Empty when the provider has none.
		Name string
		// Title is a free-text description of the session, the first
		// prompt for Codex and agy threads, which name their threads only
		// inside sqlite databases the agent never opens. Banner only.
		Title string
		// Idle is the provider's own word that the session sits unoccupied
		// (Claude Code writes an idle/waiting status for cli sessions); it
		// outranks the transcript heuristic but not a hook event.
		Idle      bool
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
		Provider Provider
		Name     string // row label: the provider's handle or the project directory
		// Title and Path describe the highlighted row in the badge's
		// banner; Title may be empty, Path is the working directory.
		Title     string
		Path      string
		Phase     Phase
		Minutes   int // time spent in the current phase, 0 when unknown
		CtxTokens uint64
	}

	// HookEvent is a session's newest hook notification, already
	// interpreted by the source: Decisive is false for events that say
	// nothing about the phase (a session start, an unknown notification).
	HookEvent struct {
		SessionID string
		Phase     Phase
		Decisive  bool
		At        time.Time
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

	// PlanUsage mirrors a provider's "plan usage" panel.
	PlanUsage struct {
		FiveHour    Limit
		Weekly      Limit
		CreditsPct  int    // extra usage credits, UnknownPct when disabled
		CreditsText string // e.g. "32.66/50"
		// Model is the weekly limit scoped to one model family (the usage API
		// calls it weekly_scoped and names the model, e.g. "Fable"); ModelLabel
		// is that name, empty when the plan has no such limit.
		Model      Limit
		ModelLabel string
		// FiveHourETA predicts when the 5-hour limit hits 100% at the
		// current burn rate. Zero when unknown or when the limit resets
		// before it would be exhausted (i.e. the forecast is not the
		// binding constraint).
		FiveHourETA time.Duration
	}

	// ProviderStats is the per-provider block of the snapshot for the
	// assistants other than the primary (Claude) one.
	ProviderStats struct {
		Provider Provider
		Chats    int
		Waiting  int
		Plan     PlanUsage
		Prompts  int // prompts sent today
	}

	// FocusTarget identifies the session a "jump to chat" button should
	// bring to the foreground on the host. SessionID lets a terminal
	// multiplexer that knows agent sessions (Herdr) find the exact pane;
	// PID and Dir serve everything else.
	FocusTarget struct {
		PID       int
		Dir       string
		SessionID string
	}

	// Snapshot is what the badge ultimately renders. Chats/Waiting/Usage/Plan
	// describe Claude Code; Extras carry the other assistants' blocks;
	// Sessions and FocusTargets are merged across providers.
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
		// Extras are the installed assistants besides Claude, in display
		// order. The badge offers one screen per entry and shows nothing
		// about an assistant that is absent from the list.
		Extras []ProviderStats
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
		Model:      Limit{Pct: UnknownPct},
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

	// PromptSource counts prompts the user sent today.
	PromptSource interface {
		PromptsToday(now time.Time) int
	}

	// PhaseResolver determines what each session is doing.
	PhaseResolver interface {
		ResolveAll(sessions []Session, now time.Time) []SessionState
	}

	// HookEventSource reports the latest hook event per session id. Each
	// assistant's event vocabulary is mapped to a Phase by the source, so
	// the resolver never sees provider-specific names.
	HookEventSource interface {
		Latest() (map[string]HookEvent, error)
	}

	// PhaseInspector is a provider's fallback phase heuristic and context
	// estimate for one session (transcript tail, conversation-db mtime,
	// rollout tail), consulted when no hook event decides the phase.
	PhaseInspector interface {
		Inspect(session Session) (Phase, uint64, error)
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
