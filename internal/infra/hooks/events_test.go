package hooks

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

	if s1 := latest["s1"]; s1.Phase != domain.PhaseWorking || !s1.Decisive || s1.At.Unix() != 200 {
		t.Errorf("s1 latest = %+v, want the UserPromptSubmit at 200 (working, decisive)", s1)
	}

	if s2 := latest["s2"]; s2.Phase != domain.PhaseWaitingInput || !s2.Decisive {
		t.Errorf("s2 latest = %+v, want Stop (waiting for input, decisive)", s2)
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

	if latest["s1"].Phase != domain.PhaseWaitingInput {
		t.Errorf("s1 latest = %+v, want Stop", latest["s1"])
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

func TestPhaseOf(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		notify   string
		want     domain.Phase
		decisive bool
	}{
		{"permission prompt", "Notification", "permission_prompt", domain.PhaseWaitingPermission, true},
		{"idle prompt", "Notification", "idle_prompt", domain.PhaseWaitingInput, true},
		{"stop", "Stop", "", domain.PhaseWaitingInput, true},
		{"agy idle", "Idle", "", domain.PhaseWaitingInput, true},
		{"codex interrupt ends the turn", "Interrupt", "", domain.PhaseWaitingInput, true},
		{"codex permission request", "PermissionRequest", "", domain.PhaseWaitingPermission, true},
		{"post tool use clears waiting", "PostToolUse", "", domain.PhaseWorking, true},
		{"prompt submitted", "UserPromptSubmit", "", domain.PhaseWorking, true},
		{"session start", "SessionStart", "", domain.PhaseWorking, true},
		{"unknown notification is not decisive", "Notification", "auth_success", domain.PhaseWorking, false},
		{"unknown event is not decisive", "SomethingNew", "", domain.PhaseWorking, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, decisive := phaseOf(tt.event, tt.notify)
			if decisive != tt.decisive {
				t.Fatalf("phaseOf() decisive = %v, want %v", decisive, tt.decisive)
			}

			if decisive && got != tt.want {
				t.Errorf("phaseOf() = %v, want %v", got, tt.want)
			}
		})
	}
}
