package claudefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func TestEventLogLatest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	content := `{"ts":100,"event":{"session_id":"s1","hook_event_name":"Notification","notification_type":"permission_prompt","message":"Bash needs permission"}}` + "\n" +
		`{"ts":200,"event":{"session_id":"s1","hook_event_name":"UserPromptSubmit"}}` + "\n" +
		`{"ts":150,"event":{"session_id":"s2","hook_event_name":"Stop"}}` + "\n" +
		"torn line not json\n" +
		`{"ts":10,"event":{"session_id":""}}` + "\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	latest, err := NewEventLog(path).Latest()
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}

	if len(latest) != 2 {
		t.Fatalf("Latest() returned %d sessions, want 2", len(latest))
	}

	if latest["s1"].Name != "UserPromptSubmit" {
		t.Errorf("s1 latest = %q, want UserPromptSubmit", latest["s1"].Name)
	}

	if latest["s2"].Name != "Stop" {
		t.Errorf("s2 latest = %q, want Stop", latest["s2"].Name)
	}
}

func TestEventLogToleratesHugeLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Legacy hook versions logged whole PostToolUse payloads: lines far
	// beyond bufio.Scanner's 64KB token limit must not break the reader.
	fat := `{"ts":100,"event":{"session_id":"s1","hook_event_name":"PostToolUse","message":"` +
		strings.Repeat("x", 200_000) + `"}}` + "\n"
	content := fat +
		`{"ts":200,"event":{"session_id":"s1","hook_event_name":"Stop"}}` + "\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	latest, err := NewEventLog(path).Latest()
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}

	if latest["s1"].Name != "Stop" {
		t.Errorf("s1 latest = %q, want Stop", latest["s1"].Name)
	}
}

func TestEventLogMissingFile(t *testing.T) {
	latest, err := NewEventLog(filepath.Join(t.TempDir(), "absent.jsonl")).Latest()
	if err != nil {
		t.Fatalf("Latest() error = %v, want nil for a missing file", err)
	}

	if latest != nil {
		t.Errorf("Latest() = %v, want nil", latest)
	}
}

func TestEventPhase(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		want     domain.Phase
		decisive bool
	}{
		{
			name:     "permission prompt",
			event:    Event{Name: "Notification", Notify: "permission_prompt"},
			want:     domain.PhaseWaitingPermission,
			decisive: true,
		},
		{
			name:     "idle prompt",
			event:    Event{Name: "Notification", Notify: "idle_prompt"},
			want:     domain.PhaseWaitingInput,
			decisive: true,
		},
		{
			name:     "stop",
			event:    Event{Name: "Stop"},
			want:     domain.PhaseWaitingInput,
			decisive: true,
		},
		{
			name:     "post tool use clears waiting",
			event:    Event{Name: "PostToolUse"},
			want:     domain.PhaseWorking,
			decisive: true,
		},
		{
			name:     "unknown notification is not decisive",
			event:    Event{Name: "Notification", Notify: "auth_success"},
			decisive: false,
		},
		{
			name:     "unknown event is not decisive",
			event:    Event{Name: "SomethingNew"},
			decisive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, decisive := tt.event.Phase()
			if decisive != tt.decisive {
				t.Fatalf("Phase() decisive = %v, want %v", decisive, tt.decisive)
			}

			if decisive && got != tt.want {
				t.Errorf("Phase() = %v, want %v", got, tt.want)
			}
		})
	}
}
