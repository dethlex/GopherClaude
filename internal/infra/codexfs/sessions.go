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

// threadIndex maps live threads to their rollout's first record. A thread's
// first record never changes, so it is located and decoded once.
type threadIndex struct {
	sessionsDir string
	logger      *slog.Logger
	metas       map[string]sessionMeta
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
		metas:       map[string]sessionMeta{},
	}

	return lockfs.NewRegistry(locksDir, registryTTL, idx.build)
}

func (x *threadIndex) build(_ *lockfs.Lister, holders []lockfs.Holder) ([]domain.Session, error) {
	sessions := make([]domain.Session, 0, len(holders))

	for _, h := range holders {
		meta, err := x.meta(h.ID)
		if err != nil {
			// A thread whose transcript is not there yet has run nothing;
			// it shows up once the file appears.
			x.logger.Debug("thread without a rollout", "thread", h.ID, "error", err)

			continue
		}

		if meta.isSubagent() {
			continue
		}

		sessions = append(sessions, domain.Session{
			Provider:  domain.ProviderCodex,
			ID:        h.ID,
			PID:       h.PID,
			Dir:       meta.CWD,
			StartedAt: meta.StartedAt,
		})
	}

	return sessions, nil
}

func (x *threadIndex) meta(threadID string) (sessionMeta, error) {
	if m, ok := x.metas[threadID]; ok {
		return m, nil
	}

	path, err := findRollout(x.sessionsDir, threadID)
	if err != nil {
		return sessionMeta{}, err
	}

	m, err := readMeta(path)
	if err != nil {
		return sessionMeta{}, err
	}

	x.metas[threadID] = m

	return m, nil
}
