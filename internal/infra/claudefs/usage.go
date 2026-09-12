package claudefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/jsonl"
)

const (
	dayLayout         = "2006-01-02"
	transcriptExt     = ".jsonl"
	assistantTypeMark = `"assistant"`
)

// UsageCollector aggregates today's token usage from transcript files under
// ~/.claude/projects. Reading is incremental: a per-file offset is kept and
// only the appended tail is parsed on each call.
//
// One API response is written to the transcript as several records (one per
// content block), each carrying a full copy of message.usage — summing
// naively overcounts ~3x, so records are deduplicated by message.id.
type UsageCollector struct {
	projectsDir string
	loc         *time.Location
	logger      *slog.Logger

	day     string
	cursors map[string]*jsonl.Cursor
	seenIDs map[string]struct{}
	totals  domain.Usage
}

func NewUsageCollector(projectsDir string, loc *time.Location, logger *slog.Logger) *UsageCollector {
	return &UsageCollector{
		projectsDir: projectsDir,
		loc:         loc,
		logger:      logger.With("module", "usage"),
		cursors:     make(map[string]*jsonl.Cursor),
		seenIDs:     make(map[string]struct{}),
	}
}

type usageEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Usage struct {
			Input         uint64 `json:"input_tokens"`
			Output        uint64 `json:"output_tokens"`
			CacheCreation uint64 `json:"cache_creation_input_tokens"`
			CacheRead     uint64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (c *UsageCollector) TodayUsage(now time.Time) (domain.Usage, error) {
	localNow := now.In(c.loc)

	day := localNow.Format(dayLayout)
	if day != c.day {
		c.reset(day)
	}

	midnight := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, c.loc)

	err := filepath.WalkDir(c.projectsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a vanished file/dir mid-walk is normal here
		}

		if entry.IsDir() || !strings.HasSuffix(path, transcriptExt) {
			return nil
		}

		info, err := entry.Info()
		if err != nil || info.ModTime().Before(midnight) {
			// Files untouched since local midnight cannot contain
			// today's records.
			return nil
		}

		if readErr := c.readTail(path); readErr != nil {
			c.logger.Warn("read transcript tail", "path", path, "error", readErr)
		}

		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c.totals, fmt.Errorf("walk projects dir %q: %w", c.projectsDir, err)
	}

	return c.totals, nil
}

func (c *UsageCollector) reset(day string) {
	c.day = day
	c.cursors = make(map[string]*jsonl.Cursor)
	c.seenIDs = make(map[string]struct{})
	c.totals = domain.Usage{}
}

// readTail parses the lines appended to the transcript since the last call.
// A rewritten file is simply read again: the message.id dedup set protects
// against double counting.
func (c *UsageCollector) readTail(path string) error {
	cur, ok := c.cursors[path]
	if !ok {
		cur = &jsonl.Cursor{}
		c.cursors[path] = cur
	}

	return cur.ReadNew(path, c.consumeLine, nil)
}

func (c *UsageCollector) consumeLine(line []byte) {
	// Cheap pre-filter: transcript lines can be megabytes; only assistant
	// records carry usage.
	if !bytes.Contains(line, []byte(assistantTypeMark)) {
		return
	}

	var entry usageEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		c.logger.Debug("skip malformed transcript line", "error", err)

		return
	}

	if entry.Type != "assistant" || entry.Message.ID == "" {
		return
	}

	ts, err := time.Parse(time.RFC3339, entry.Timestamp)
	if err != nil {
		return
	}

	if ts.In(c.loc).Format(dayLayout) != c.day {
		return
	}

	if _, dup := c.seenIDs[entry.Message.ID]; dup {
		return
	}

	c.seenIDs[entry.Message.ID] = struct{}{}

	c.totals.Input += entry.Message.Usage.Input
	c.totals.Output += entry.Message.Usage.Output
	c.totals.CacheCreation += entry.Message.Usage.CacheCreation
	c.totals.CacheRead += entry.Message.Usage.CacheRead
}
