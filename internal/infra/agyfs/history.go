package agyfs

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/jsonl"
)

// History reads agy's history.jsonl (one record per prompt: {"timestamp":
// <unix ms>, "conversationId", "display", "workspace"}). Today's prompt
// count rereads the file, it is small (tens of KB); the first prompt per
// conversation is accumulated through a cursor because it must never be
// forgotten while the conversation lives.
type History struct {
	path   string
	loc    *time.Location
	logger *slog.Logger

	firstPrompts map[string]string
	cursor       jsonl.Cursor
}

var _ domain.PromptSource = (*History)(nil)

func NewHistory(path string, loc *time.Location, logger *slog.Logger) *History {
	return &History{
		path:         path,
		loc:          loc,
		logger:       logger.With("module", "agy-history"),
		firstPrompts: make(map[string]string),
	}
}

type historyRecord struct {
	Timestamp      int64  `json:"timestamp"`
	ConversationID string `json:"conversationId"`
	Display        string `json:"display"`
}

// FirstPrompt is what the user first asked in a conversation, the closest
// thing agy keeps on disk to a thread title (its names live in sqlite).
// Empty until the conversation's first record appears.
func (h *History) FirstPrompt(conversationID string) string {
	err := h.cursor.ReadNew(h.path, h.rememberFirst, func() { clear(h.firstPrompts) })
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.logger.Debug("read history", "error", err)
	}

	return h.firstPrompts[conversationID]
}

func (h *History) rememberFirst(line []byte) {
	var rec historyRecord
	if err := json.Unmarshal(line, &rec); err != nil || rec.ConversationID == "" || rec.Display == "" {
		return
	}

	if _, seen := h.firstPrompts[rec.ConversationID]; !seen {
		h.firstPrompts[rec.ConversationID] = rec.Display
	}
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
