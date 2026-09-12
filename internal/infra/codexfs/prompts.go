package codexfs

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/jsonl"
)

const (
	rolloutExt = ".jsonl"
	dayLayout  = "2006-01-02"

	// A busy day means megabytes of transcripts; the count is cached and
	// recounted on this cadence (the badge shows it as a plain number).
	promptsTTL = 60 * time.Second
)

// rolloutCursor is how far one rollout has been read and how many of today's
// prompts it held so far. Rollouts are append-only, so a pass only parses the
// bytes Codex added since the last one; a Desktop thread's transcript runs to
// 100 MB, and re-reading it every minute would stall the frame loop.
type rolloutCursor struct {
	cur   jsonl.Cursor
	count int
}

// PromptCounter counts today's prompts across all rollouts: Codex no longer
// writes history.jsonl, but every prompt is a user message in its thread's
// transcript. A thread started days ago but used today lives under its start
// date, so the walk covers every date and reads only files modified today.
// Each file is read incrementally from its last cursor position.
type PromptCounter struct {
	sessionsDir string
	loc         *time.Location
	logger      *slog.Logger

	files map[string]*rolloutCursor

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
		files:       make(map[string]*rolloutCursor),
	}
}

func (c *PromptCounter) PromptsToday(now time.Time) int {
	local := now.In(c.loc)
	day := local.Format(dayLayout)

	if day == c.cachedDay && now.Sub(c.cachedAt) < promptsTTL {
		return c.cached
	}

	if day != c.cachedDay {
		for _, cur := range c.files {
			cur.count = 0
		}
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
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, rolloutExt) {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.ModTime().Before(midnight) {
			return nil
		}

		cur, ok := c.files[path]
		if !ok {
			cur = &rolloutCursor{}
			c.files[path] = cur
		}

		onLine := func(line []byte) {
			if isPromptOn(line, day, c.loc) {
				cur.count++
			}
		}

		// A rewritten file starts its count over.
		onReset := func() { cur.count = 0 }

		if err := cur.cur.ReadNew(path, onLine, onReset); err != nil {
			c.logger.Debug("count prompts", "file", path, "error", err)
		}

		return nil
	}

	if err := filepath.WalkDir(c.sessionsDir, walk); err != nil {
		c.logger.Debug("walk sessions", "error", err)
	}

	total := 0
	for _, cur := range c.files {
		total += cur.count
	}

	return total
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
