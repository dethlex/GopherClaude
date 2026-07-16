package claudefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func writeTranscript(t *testing.T, dir, cwd, sessionID, content string) {
	t.Helper()

	project := filepath.Join(dir, sanitizeProjectDir(cwd))
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPhase(t *testing.T) {
	const (
		cwd       = "/Users/x/proj"
		sessionID = "sess-1"
	)

	tests := []struct {
		name    string
		content string
		want    domain.Phase
	}{
		{
			name: "end_turn with trailers means waiting",
			content: `{"type":"assistant","message":{"stop_reason":"end_turn"}}` + "\n" +
				`{"type":"mode","mode":"normal"}` + "\n" +
				`{"type":"last-prompt","leafUuid":"u1"}` + "\n",
			want: domain.PhaseWaitingInput,
		},
		{
			name:    "tool_use means working",
			content: `{"type":"assistant","message":{"stop_reason":"tool_use"}}` + "\n",
			want:    domain.PhaseWorking,
		},
		{
			name: "user tool_result means working",
			content: `{"type":"assistant","message":{"stop_reason":"tool_use"}}` + "\n" +
				`{"type":"user","message":{"content":[{"type":"tool_result"}]}}` + "\n",
			want: domain.PhaseWorking,
		},
		{
			name: "stop hook summary means waiting",
			content: `{"type":"assistant","message":{"stop_reason":"end_turn"}}` + "\n" +
				`{"type":"system","subtype":"stop_hook_summary"}` + "\n",
			want: domain.PhaseWaitingInput,
		},
		{
			name:    "empty transcript means working",
			content: "",
			want:    domain.PhaseWorking,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTranscript(t, dir, cwd, sessionID, tt.content)

			transcripts := NewTranscriptDir(dir)

			got, _, err := transcripts.Inspect(domain.Session{ID: sessionID, Dir: cwd})
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}

			if got != tt.want {
				t.Errorf("Inspect() phase = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTranscriptPhaseMissingFile(t *testing.T) {
	transcripts := NewTranscriptDir(t.TempDir())

	got, _, err := transcripts.Inspect(domain.Session{ID: "nope", Dir: "/Users/x/proj"})
	if err == nil {
		t.Error("Inspect() expected an error for a missing transcript")
	}

	if got != domain.PhaseWorking {
		t.Errorf("Inspect() phase = %v, want PhaseWorking on error", got)
	}
}

func TestTranscriptCtxTokens(t *testing.T) {
	const (
		cwd       = "/Users/x/proj"
		sessionID = "sess-ctx"
	)

	dir := t.TempDir()

	// The latest assistant record defines the context size even when a
	// user tool_result follows it.
	content := `{"type":"assistant","message":{"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}` + "\n" +
		`{"type":"assistant","message":{"stop_reason":"tool_use","usage":{"input_tokens":50000,"output_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":300000}}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result"}]}}` + "\n"

	writeTranscript(t, dir, cwd, sessionID, content)

	phase, ctx, err := NewTranscriptDir(dir).Inspect(domain.Session{ID: sessionID, Dir: cwd})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}

	if phase != domain.PhaseWorking {
		t.Errorf("phase = %v, want PhaseWorking", phase)
	}

	if want := uint64(50000 + 1000 + 2000 + 300000); ctx != want {
		t.Errorf("ctx = %d, want %d", ctx, want)
	}
}

func TestSanitizeProjectDir(t *testing.T) {
	got := sanitizeProjectDir("/Users/lexis/GolandProjects/ClaudeControl")
	want := "-Users-lexis-GolandProjects-ClaudeControl"

	if got != want {
		t.Errorf("sanitizeProjectDir() = %q, want %q", got, want)
	}

	if got := sanitizeProjectDir("/Users/x/app.v2_dev"); got != "-Users-x-app-v2-dev" {
		t.Errorf("sanitizeProjectDir() = %q, want %q", got, "-Users-x-app-v2-dev")
	}
}
