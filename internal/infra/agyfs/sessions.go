// Package agyfs reads Antigravity CLI (agy) state from the local filesystem
// and process table: live sessions, prompt history and per-conversation
// activity.
package agyfs

import (
	"log/slog"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/lockfs"
)

// The registry is rebuilt from lsof, which costs ~0.6s on a busy machine; it
// is cached and refreshed on this cadence.
const registryTTL = 10 * time.Second

// NewSessionRegistry lists live agy sessions: agy keeps one open
// presence/<conversationId>.lock per running interactive process and writes
// nothing else about the session to disk, so the working directory comes
// from the process itself (lsof again).
func NewSessionRegistry(presenceDir string, logger *slog.Logger) *lockfs.Registry {
	log := logger.With("module", "agy-sessions")

	return lockfs.NewRegistry(presenceDir, registryTTL, func(l *lockfs.Lister, holders []lockfs.Holder) ([]domain.Session, error) {
		cwds, err := l.CWDs(uniquePIDs(holders))
		if err != nil {
			log.Warn("lsof cwd", "error", err)
		}

		sessions := make([]domain.Session, 0, len(holders))
		for _, h := range holders {
			sessions = append(sessions, domain.Session{
				Provider: domain.ProviderAntigravity,
				ID:       h.ID,
				PID:      h.PID,
				Dir:      cwds[h.PID],
			})
		}

		return sessions, nil
	})
}

// uniquePIDs keeps the holders' order while dropping repeats: lsof -p takes
// a list and one process may hold several locks.
func uniquePIDs(holders []lockfs.Holder) []int {
	pids := make([]int, 0, len(holders))
	seen := make(map[int]bool, len(holders))

	for _, h := range holders {
		if seen[h.PID] {
			continue
		}

		seen[h.PID] = true
		pids = append(pids, h.PID)
	}

	return pids
}
