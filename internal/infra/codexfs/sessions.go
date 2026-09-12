package codexfs

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// The registry is rebuilt from lsof, which costs ~0.6s on a busy
	// machine; it is cached and refreshed on this cadence.
	registryTTL = 10 * time.Second

	lockExt = ".lock"
)

// lockHolder is one process holding one thread lock.
type lockHolder struct {
	pid      int
	threadID string
}

// SessionRegistry lists live Codex threads: the processes holding a
// thread-writer-locks/<threadId>.lock open (the TUI, `codex exec`, or the
// app-server behind Codex Desktop, which holds one lock per open thread).
// Lock files outlive their process, so only lsof tells which are alive. The
// working directory and start time come from the thread's rollout; threads
// spawned by other threads (sub-agents) are skipped.
type SessionRegistry struct {
	locksDir    string
	sessionsDir string
	logger      *slog.Logger
	now         func() time.Time

	// Exec seam for tests.
	runLsof func(args ...string) ([]byte, error)

	// A thread's first record never changes, so it is decoded once: a
	// refresh then costs one lsof and nothing else.
	metas map[string]sessionMeta

	cached   []domain.Session
	cachedAt time.Time
}

var _ domain.SessionSource = (*SessionRegistry)(nil)

func NewSessionRegistry(locksDir, sessionsDir string, logger *slog.Logger) *SessionRegistry {
	return &SessionRegistry{
		locksDir:    locksDir,
		sessionsDir: sessionsDir,
		logger:      logger.With("module", "codex-sessions"),
		now:         time.Now,
		runLsof: func(args ...string) ([]byte, error) {
			// lsof exits 1 when some files have no opener; that is not an error here.
			out, _ := exec.Command("lsof", args...).Output()

			return out, nil
		},
		metas: map[string]sessionMeta{},
	}
}

func (r *SessionRegistry) Sessions() ([]domain.Session, error) {
	now := r.now()
	if r.cached != nil && now.Sub(r.cachedAt) < registryTTL {
		return r.cached, nil
	}

	sessions, err := r.load()
	if err != nil {
		return r.cached, err
	}

	r.cached = sessions
	r.cachedAt = now

	return sessions, nil
}

func (r *SessionRegistry) load() ([]domain.Session, error) {
	out, err := r.runLsof("-F", "pn", "+D", r.locksDir)
	if err != nil {
		return nil, fmt.Errorf("lsof locks dir: %w", err)
	}

	holders := parseLockHolders(out)
	sessions := make([]domain.Session, 0, len(holders))

	for _, h := range holders {
		meta, err := r.meta(h.threadID)
		if err != nil {
			// A thread whose transcript is not there yet has run nothing;
			// it shows up once the file appears.
			r.logger.Debug("thread without a rollout", "thread", h.threadID, "error", err)

			continue
		}

		if meta.isSubagent() {
			continue
		}

		sessions = append(sessions, domain.Session{
			Provider:  domain.ProviderCodex,
			ID:        h.threadID,
			PID:       h.pid,
			Dir:       meta.CWD,
			StartedAt: meta.StartedAt,
		})
	}

	return sessions, nil
}

// meta returns a thread's first record, located and decoded once.
func (r *SessionRegistry) meta(threadID string) (sessionMeta, error) {
	if m, ok := r.metas[threadID]; ok {
		return m, nil
	}

	path, err := findRollout(r.sessionsDir, threadID)
	if err != nil {
		return sessionMeta{}, err
	}

	m, err := readMeta(path)
	if err != nil {
		return sessionMeta{}, err
	}

	r.metas[threadID] = m

	return m, nil
}

// parseLockHolders parses `lsof -F pn` output — a "p<pid>" line starts a
// process block, each "n<name>" line is a file it holds — keeping every
// <threadId>.lock and skipping the rest of the directory: the
// .coordination.lock Codex uses for its own bookkeeping and the directory
// itself, which some process may hold open. Sorted by pid, then thread id.
func parseLockHolders(out []byte) []lockHolder {
	var (
		holders []lockHolder
		pid     int
		seen    = make(map[lockHolder]bool)
	)

	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if len(line) < 2 {
			continue
		}

		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(string(line[1:]))
		case 'n':
			base := filepath.Base(string(line[1:]))
			if pid == 0 || strings.HasPrefix(base, ".") || !strings.HasSuffix(base, lockExt) {
				continue
			}

			// One process may hold the same lock through several
			// descriptors; that is still one thread.
			holder := lockHolder{pid: pid, threadID: strings.TrimSuffix(base, lockExt)}
			if seen[holder] {
				continue
			}

			seen[holder] = true
			holders = append(holders, holder)
		}
	}

	sort.Slice(holders, func(i, j int) bool {
		if holders[i].pid != holders[j].pid {
			return holders[i].pid < holders[j].pid
		}

		return holders[i].threadID < holders[j].threadID
	})

	return holders
}
