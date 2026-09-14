// Package codexfs reads OpenAI Codex state from the local filesystem and the
// process table: live threads, their transcripts (rollouts) and today's
// prompts. Codex keeps everything under $CODEX_HOME (~/.codex).
package codexfs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// Rollouts live in sessions/YYYY/MM/DD/rollout-<start>-<threadId>.jsonl;
	// the date is the thread's start, so a lookup by id globs every date.
	rolloutGlob = "*/*/*/rollout-*-%s.jsonl"

	// The tail is enough for the newest task event and token count; whole
	// rollouts run to megabytes.
	tailReadSize = 64 * 1024

	timestampLayout = time.RFC3339Nano

	recSessionMeta = "session_meta"
	recEventMsg    = "event_msg"
	recResponse    = "response_item"

	evTaskStarted  = "task_started"
	evTaskComplete = "task_complete"
	evTurnAborted  = "turn_aborted"
	evTokenCount   = "token_count"

	itemMessage = "message"
	roleUser    = "user"

	// Injected context (environment, instructions, plugin lists) arrives as
	// user messages whose text is an XML-ish block; real prompts do not
	// start like that.
	injectedPrefix = "<"

	// The first prompt sits right after session_meta and turn_context;
	// scanning further into a 100 MB Desktop rollout would stall the
	// registry for nothing.
	headScanLimit = 256 * 1024

	// Individual rollout lines (turn_context with instructions) run to
	// tens of KB; the scanner needs room for them.
	headLineLimit = 128 * 1024
)

// record is the envelope of every rollout line; the payload is decoded per
// type. The timestamp stays a string: only the prompt counter needs it, and
// a record with an odd timestamp must not hide its type from the others.
type record struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// sessionMeta is the first record of a rollout.
type sessionMeta struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
	// Source is a string ("cli", "exec", "vscode") for the user's threads
	// and an object such as {"subagent":"review"} for threads spawned by
	// another thread.
	Source json.RawMessage `json:"source"`

	// StartedAt is the envelope timestamp of the record.
	StartedAt time.Time `json:"-"`
}

// rolloutHead is what the registry keeps per thread: its first record and
// the user's first prompt, the thread's title as far as the disk knows.
type rolloutHead struct {
	meta        sessionMeta
	firstPrompt string
}

// isSubagent reports whether the thread belongs to another thread rather
// than to the user; those are not chats to count or list.
func (m sessionMeta) isSubagent() bool {
	trimmed := bytes.TrimSpace(m.Source)

	return len(trimmed) > 0 && trimmed[0] == '{'
}

// eventPayload covers the event_msg payloads the badge reads.
type eventPayload struct {
	Type string `json:"type"`
	Info struct {
		Last struct {
			Total uint64 `json:"total_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`
}

// messagePayload is a response_item of type message.
type messagePayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// userPrompt extracts the text of a record when it is a prompt the user
// typed; injected context blocks and every other record yield false.
func userPrompt(rec record) (string, bool) {
	if rec.Type != recResponse {
		return "", false
	}

	var msg messagePayload
	if err := json.Unmarshal(rec.Payload, &msg); err != nil || msg.Type != itemMessage || msg.Role != roleUser {
		return "", false
	}

	if len(msg.Content) == 0 || msg.Content[0].Text == "" || strings.HasPrefix(msg.Content[0].Text, injectedPrefix) {
		return "", false
	}

	return msg.Content[0].Text, true
}

// findRollout locates a thread's transcript; the newest match wins should a
// thread ever be re-recorded.
func findRollout(sessionsDir, threadID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(sessionsDir, fmt.Sprintf(rolloutGlob, threadID)))
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", os.ErrNotExist
	}

	sort.Strings(matches)

	return matches[len(matches)-1], nil
}

// readHead decodes a rollout's session_meta and looks for the user's first
// prompt within the next headScanLimit bytes; firstPrompt stays empty when
// none is there yet.
func readHead(path string) (rolloutHead, error) {
	f, err := os.Open(path)
	if err != nil {
		return rolloutHead{}, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, headLineLimit)

	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return rolloutHead{}, err
	}

	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return rolloutHead{}, fmt.Errorf("decode first record of %q: %w", path, err)
	}

	if rec.Type != recSessionMeta {
		return rolloutHead{}, fmt.Errorf("first record of %q is %q, want %s", path, rec.Type, recSessionMeta)
	}

	var head rolloutHead
	if err := json.Unmarshal(rec.Payload, &head.meta); err != nil {
		return rolloutHead{}, fmt.Errorf("decode session_meta of %q: %w", path, err)
	}

	head.meta.StartedAt, _ = parseTimestamp(rec.Timestamp)

	scanner := bufio.NewScanner(io.LimitReader(reader, headScanLimit))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), headLineLimit)

	for scanner.Scan() {
		var rec record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}

		if text, ok := userPrompt(rec); ok {
			head.firstPrompt = text

			break
		}
	}

	// A scanner error here is a torn or overlong line inside the head:
	// the meta is good and the prompt simply stays unknown for now.
	return head, nil
}

// parseTimestamp reads a rollout timestamp ("2026-09-08T11:42:15.387Z").
func parseTimestamp(s string) (time.Time, error) {
	return time.Parse(timestampLayout, s)
}
