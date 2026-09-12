package agyfs

import (
	"os"
	"path/filepath"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// workingWindow: a conversation database modified this recently means agy is
// still producing output for it.
const workingWindow = 30 * time.Second

// ConversationDir is the phase heuristic for agy sessions that have no hook
// events yet. agy writes conversations/<conversationId>.db only while a turn
// is in progress, so a fresh mtime means "working" and a stale one "waiting".
// It cannot see permission prompts (only hooks can) and knows no context size.
type ConversationDir struct {
	dir string
	now func() time.Time
}

var _ domain.PhaseInspector = (*ConversationDir)(nil)

func NewConversationDir(dir string) *ConversationDir {
	return &ConversationDir{dir: dir, now: time.Now}
}

// Inspect implements domain.PhaseInspector.
func (c *ConversationDir) Inspect(session domain.Session) (domain.Phase, uint64, error) {
	info, err := os.Stat(filepath.Join(c.dir, session.ID+".db"))
	if err != nil {
		// No database yet: the conversation has not produced a turn, so
		// nothing is running.
		return domain.PhaseWaitingInput, 0, nil
	}

	if c.now().Sub(info.ModTime()) < workingWindow {
		return domain.PhaseWorking, 0, nil
	}

	return domain.PhaseWaitingInput, 0, nil
}
