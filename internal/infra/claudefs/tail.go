package claudefs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	tailReadSize = 64 * 1024

	stopReasonToolUse = "tool_use"
)

// TranscriptDir inspects the tail of a session's transcript for two things:
//
//   - the phase heuristic — the fallback for sessions that started before the
//     hooks were installed: a final assistant record with stop_reason=end_turn
//     means the turn is over and Claude waits for the user, while a trailing
//     tool_use (or a user tool_result) means work is still in flight; a
//     pending permission dialog looks exactly the same, only hooks can tell;
//   - the context size — the token accounting of the latest assistant
//     response (input + cache + output) approximates how full the session's
//     context window is.
type TranscriptDir struct {
	dir string
}

var _ domain.PhaseInspector = (*TranscriptDir)(nil)

func NewTranscriptDir(dir string) *TranscriptDir {
	return &TranscriptDir{dir: dir}
}

type tailEntry struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input         uint64 `json:"input_tokens"`
			Output        uint64 `json:"output_tokens"`
			CacheCreation uint64 `json:"cache_creation_input_tokens"`
			CacheRead     uint64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Inspect reads the transcript tail and returns the heuristic phase plus the
// session's approximate context size in tokens (0 when not derivable).
func (t *TranscriptDir) Inspect(session domain.Session) (domain.Phase, uint64, error) {
	path := filepath.Join(t.dir, sanitizeProjectDir(session.Dir), session.ID+transcriptExt)

	tail, err := readFileTail(path, tailReadSize)
	if err != nil {
		return domain.PhaseWorking, 0, fmt.Errorf("read transcript %q: %w", path, err)
	}

	var (
		phase     = domain.PhaseWorking
		phaseSet  bool
		ctxTokens uint64
	)

	lines := bytes.Split(tail, []byte{'\n'})

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if len(line) == 0 {
			continue
		}

		var entry tailEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // first chunk after seek may be a partial line
		}

		switch entry.Type {
		case "assistant":
			if !phaseSet {
				phaseSet = true

				if entry.Message.StopReason != stopReasonToolUse && entry.Message.StopReason != "" {
					phase = domain.PhaseWaitingInput
				}
			}

			if ctxTokens == 0 {
				u := entry.Message.Usage
				ctxTokens = u.Input + u.Output + u.CacheCreation + u.CacheRead
			}
		case "user":
			phaseSet = true
		case "system":
			if !phaseSet && entry.Subtype == "stop_hook_summary" {
				// Stop hooks already ran: the turn is over.
				phaseSet = true
				phase = domain.PhaseWaitingInput
			}
		default:
			// Trailer records (mode, last-prompt, custom-title, ...)
			// say nothing; keep walking up.
		}

		if phaseSet && ctxTokens > 0 {
			break
		}
	}

	return phase, ctxTokens, nil
}

// sanitizeProjectDir mirrors how Claude Code names per-project transcript
// directories: every character outside [A-Za-z0-9-] becomes '-'
// (/Users/x/Proj.x -> -Users-x-Proj-x).
func sanitizeProjectDir(cwd string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, cwd)
}

// readFileTail returns up to maxBytes from the end of the file.
func readFileTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	return io.ReadAll(f)
}
