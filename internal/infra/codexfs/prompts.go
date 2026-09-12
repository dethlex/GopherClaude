package codexfs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	rolloutExt = ".jsonl"
	dayLayout  = "2006-01-02"

	// A busy day means megabytes of transcripts; the count is cached and
	// recounted on this cadence (the badge shows it as a plain number).
	promptsTTL = 60 * time.Second
)

// PromptCounter counts today's prompts across all rollouts: Codex no longer
// writes history.jsonl, but every prompt is a user message in its thread's
// transcript. A thread started days ago but used today lives under its start
// date, so the walk covers every date and reads only files modified today.
type PromptCounter struct {
	sessionsDir string
	loc         *time.Location
	logger      *slog.Logger

	cached    int
	cachedDay string
	cachedAt  time.Time
}

var _ domain.PromptSource = (*PromptCounter)(nil)

func NewPromptCounter(sessionsDir string, loc *time.Location, logger *slog.Logger) *PromptCounter {
	return &PromptCounter{
		sessionsDir: sessionsDir,
		loc:         loc,
		logger:      logger.With("module", "codex-prompts"),
	}
}

func (c *PromptCounter) PromptsToday(now time.Time) int {
	local := now.In(c.loc)
	day := local.Format(dayLayout)

	if day == c.cachedDay && now.Sub(c.cachedAt) < promptsTTL {
		return c.cached
	}

	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.loc)

	c.cached = c.count(midnight, day)
	c.cachedDay = day
	c.cachedAt = now

	return c.cached
}

// count walks the sessions tree by Stat alone and reads only the rollouts
// modified since midnight.
func (c *PromptCounter) count(midnight time.Time, day string) int {
	total := 0

	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, rolloutExt) {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.ModTime().Before(midnight) {
			return nil
		}

		n, err := countUserMessages(path, day, c.loc)
		if err != nil {
			c.logger.Debug("count prompts", "file", path, "error", err)
		}

		total += n

		return nil
	}

	if err := filepath.WalkDir(c.sessionsDir, walk); err != nil {
		c.logger.Debug("walk sessions", "error", err)
	}

	return total
}

// countUserMessages counts the real prompts in one rollout: user-role
// messages dated today whose text is not injected context. Lines can exceed
// bufio.Scanner's limit (tool outputs are inlined), hence the Reader.
func countUserMessages(path, day string, loc *time.Location) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	count := 0
	reader := bufio.NewReader(f)

	for {
		line, err := reader.ReadBytes('\n')

		if len(bytes.TrimSpace(line)) > 0 && isPromptOn(line, day, loc) {
			count++
		}

		if errors.Is(err, io.EOF) {
			return count, nil
		}

		if err != nil {
			return count, err
		}
	}
}

// isPromptOn reports whether the line is a user prompt sent on the given day.
func isPromptOn(line []byte, day string, loc *time.Location) bool {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != recResponse {
		return false
	}

	ts, err := parseTimestamp(rec.Timestamp)
	if err != nil || ts.In(loc).Format(dayLayout) != day {
		return false
	}

	var msg messagePayload
	if err := json.Unmarshal(rec.Payload, &msg); err != nil || msg.Type != itemMessage || msg.Role != roleUser {
		return false
	}

	return len(msg.Content) > 0 && msg.Content[0].Text != "" && !strings.HasPrefix(msg.Content[0].Text, injectedPrefix)
}
