package claudefs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	offsets map[string]int64
	seenIDs map[string]struct{}
	totals  domain.Usage
}

func NewUsageCollector(projectsDir string, loc *time.Location, logger *slog.Logger) *UsageCollector {
	return &UsageCollector{
		projectsDir: projectsDir,
		loc:         loc,
		logger:      logger.With("module", "usage"),
		offsets:     make(map[string]int64),
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

		if readErr := c.readTail(path, info.Size()); readErr != nil {
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
	c.offsets = make(map[string]int64)
	c.seenIDs = make(map[string]struct{})
	c.totals = domain.Usage{}
}

// readTail parses complete lines appended to the file since the last call and
// advances the stored offset. A trailing partial line (still being written by
// Claude Code) is left for the next call.
func (c *UsageCollector) readTail(path string, size int64) error {
	offset := c.offsets[path]
	if size < offset {
		// The file was truncated or replaced; reread it. The message.id
		// dedup set protects against double counting.
		offset = 0
	}

	if size == offset {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to %d: %w", offset, err)
	}

	reader := bufio.NewReaderSize(f, 256*1024)

	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break // partial line: re-read next tick
		}

		if err != nil {
			return fmt.Errorf("read line: %w", err)
		}

		offset += int64(len(line))

		c.consumeLine(line)
	}

	c.offsets[path] = offset

	return nil
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
