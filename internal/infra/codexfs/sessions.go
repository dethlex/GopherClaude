package codexfs

import (
	"log/slog"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/lockfs"
)

// The registry is rebuilt from lsof, which costs ~0.6s on a busy machine; it
// is cached and refreshed on this cadence.
const registryTTL = 10 * time.Second

// threadIndex maps live threads to their rollout's head. The first record
// never changes; the first prompt may not exist yet when the thread appears
// (Desktop opens a thread before the first message), so a head without one
// is read again on the next build.
type threadIndex struct {
	sessionsDir string
	logger      *slog.Logger
	heads       map[string]rolloutHead
}

// NewSessionRegistry lists live Codex threads: the processes holding a
// thread-writer-locks/<threadId>.lock open (the TUI, `codex exec`, or the
// app-server behind Codex Desktop, which holds one lock per open thread). The
// working directory and start time come from the thread's rollout; threads
// spawned by other threads (sub-agents) are skipped.
func NewSessionRegistry(locksDir, sessionsDir string, logger *slog.Logger) *lockfs.Registry {
	idx := &threadIndex{
		sessionsDir: sessionsDir,
		logger:      logger.With("module", "codex-sessions"),
		heads:       map[string]rolloutHead{},
	}

	return lockfs.NewRegistry(locksDir, registryTTL, idx.build)
}

func (x *threadIndex) build(_ *lockfs.Lister, holders []lockfs.Holder) ([]domain.Session, error) {
	sessions := make([]domain.Session, 0, len(holders))

	for _, h := range holders {
		head, err := x.head(h.ID)
		if err != nil {
			// A thread whose transcript is not there yet has run nothing;
			// it shows up once the file appears.
			x.logger.Debug("thread without a rollout", "thread", h.ID, "error", err)

			continue
		}

		if head.meta.isSubagent() {
			continue
		}

		sessions = append(sessions, domain.Session{
			Provider:  domain.ProviderCodex,
			ID:        h.ID,
			PID:       h.PID,
			Dir:       head.meta.CWD,
			Title:     head.firstPrompt,
			StartedAt: head.meta.StartedAt,
		})
	}

	return sessions, nil
}

func (x *threadIndex) head(threadID string) (rolloutHead, error) {
	if h, ok := x.heads[threadID]; ok && h.firstPrompt != "" {
		return h, nil
	}

	path, err := findRollout(x.sessionsDir, threadID)
	if err != nil {
		return rolloutHead{}, err
	}

	h, err := readHead(path)
	if err != nil {
		return rolloutHead{}, err
	}

	x.heads[threadID] = h

	return h, nil
}
