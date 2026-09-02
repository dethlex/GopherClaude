package agyfs

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// History counts today's prompts from agy's history.jsonl (one record per
// prompt: {"timestamp": <unix ms>, "conversationId", "workspace", ...}).
// The file is small (tens of KB), so it is reread on every call.
type History struct {
	path   string
	loc    *time.Location
	logger *slog.Logger
}

var _ domain.PromptSource = (*History)(nil)

func NewHistory(path string, loc *time.Location, logger *slog.Logger) *History {
	return &History{path: path, loc: loc, logger: logger.With("module", "agy-history")}
}

type historyRecord struct {
	Timestamp int64 `json:"timestamp"`
}

func (h *History) PromptsToday(now time.Time) int {
	f, err := os.Open(h.path)
	if err != nil {
		return 0 // agy not installed / never used: nothing to count
	}
	defer f.Close()

	day := now.In(h.loc).Format("2006-01-02")
	count := 0

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec historyRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil || rec.Timestamp == 0 {
			continue
		}

		if time.UnixMilli(rec.Timestamp).In(h.loc).Format("2006-01-02") == day {
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		h.logger.Debug("scan history", "error", err)
	}

	return count
}
