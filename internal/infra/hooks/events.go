// Package hooks reads the event log the badge hook script appends to
// (~/.claude-badge/events.jsonl): one record per hook invocation from Claude
// Code, Antigravity or Codex, reduced to the fields the agent needs.
package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Hook event names as the assistants report them on the hook's stdin.
const (
	hookNotification      = "Notification"
	hookStop              = "Stop"
	hookUserPromptSubmit  = "UserPromptSubmit"
	hookPostToolUse       = "PostToolUse"
	hookSessionStart      = "SessionStart"
	hookIdle              = "Idle"              // agy: waiting for input
	hookPermissionRequest = "PermissionRequest" // Codex: an approval dialog is open
	hookInterrupt         = "Interrupt"         // Codex: the user cut the turn short

	notifyPermission = "permission_prompt"
	notifyIdle       = "idle_prompt"
)

// phaseOf maps an event name (and, for notifications, the kind) to a session
// phase. The second value is false for events that say nothing about it.
func phaseOf(name, notify string) (domain.Phase, bool) {
	switch name {
	case hookNotification:
		switch notify {
		case notifyPermission:
			return domain.PhaseWaitingPermission, true
		case notifyIdle:
			return domain.PhaseWaitingInput, true
		default:
			return domain.PhaseWorking, false
		}
	case hookPermissionRequest:
		return domain.PhaseWaitingPermission, true
	case hookStop, hookIdle, hookInterrupt:
		return domain.PhaseWaitingInput, true
	case hookUserPromptSubmit, hookPostToolUse, hookSessionStart:
		return domain.PhaseWorking, true
	default:
		return domain.PhaseWorking, false
	}
}

// EventLog reads the events file appended by the hook script installed via
// `make install-hooks`. The file is small (the script rotates it at ~1 MB),
// so it is reread on every tick.
type EventLog struct {
	path string
}

var _ domain.HookEventSource = (*EventLog)(nil)

func NewEventLog(path string) *EventLog {
	return &EventLog{path: path}
}

type eventLine struct {
	TS    int64 `json:"ts"`
	Event struct {
		SessionID        string `json:"session_id"`
		HookEventName    string `json:"hook_event_name"`
		NotificationType string `json:"notification_type"`
	} `json:"event"`
}

// Latest returns the most recent event per session.
func (l *EventLog) Latest() (map[string]domain.HookEvent, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // hooks not installed: heuristics take over
	}

	if err != nil {
		return nil, fmt.Errorf("open events log %q: %w", l.path, err)
	}
	defer f.Close()

	latest := make(map[string]domain.HookEvent)

	// bufio.Reader instead of Scanner: legacy hook versions logged whole
	// PostToolUse payloads, producing lines far beyond Scanner's token
	// limit. Those lines still parse (or are skipped), never error.
	reader := bufio.NewReader(f)

	for {
		raw, err := reader.ReadBytes('\n')

		if len(raw) > 0 {
			var line eventLine
			if jsonErr := json.Unmarshal(raw, &line); jsonErr == nil && line.Event.SessionID != "" {
				phase, decisive := phaseOf(line.Event.HookEventName, line.Event.NotificationType)

				event := domain.HookEvent{
					SessionID: line.Event.SessionID,
					Phase:     phase,
					Decisive:  decisive,
					At:        time.Unix(line.TS, 0),
				}

				if !event.At.Before(latest[event.SessionID].At) {
					latest[event.SessionID] = event
				}
			}
		}

		if errors.Is(err, io.EOF) {
			return latest, nil
		}

		if err != nil {
			return latest, fmt.Errorf("read events log: %w", err)
		}
	}
}
