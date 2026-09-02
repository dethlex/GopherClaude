// Package agyfs reads Antigravity CLI (agy) state from the local filesystem
// and process table: live sessions, prompt history and per-conversation
// activity.
package agyfs

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

	lockExt   = ".lock"
	modelFlag = "--model"
)

// SessionRegistry lists live agy sessions. agy keeps one open
// presence/<conversationId>.lock per running interactive process, so the
// mapping pid -> conversation comes from the process table via lsof (the
// lock files themselves are empty). Working directories come from lsof too,
// the model from the process arguments.
type SessionRegistry struct {
	presenceDir string
	logger      *slog.Logger
	now         func() time.Time

	// Exec seams for tests.
	runLsof func(args ...string) ([]byte, error)
	runPs   func(pids []int) ([]byte, error)

	cached   []domain.Session
	cachedAt time.Time
}

func NewSessionRegistry(presenceDir string, logger *slog.Logger) *SessionRegistry {
	return &SessionRegistry{
		presenceDir: presenceDir,
		logger:      logger.With("module", "agy-sessions"),
		now:         time.Now,
		runLsof: func(args ...string) ([]byte, error) {
			// lsof exits 1 when some files have no opener; that is not an error here.
			out, _ := exec.Command("lsof", args...).Output()

			return out, nil
		},
		runPs: func(pids []int) ([]byte, error) {
			return exec.Command("ps", "-o", "pid=,args=", "-p", joinPIDs(pids)).Output()
		},
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
	out, err := r.runLsof("-F", "pn", "+D", r.presenceDir)
	if err != nil {
		return nil, fmt.Errorf("lsof presence dir: %w", err)
	}

	locks := lockHolders(parseLsofPN(out))
	if len(locks) == 0 {
		return []domain.Session{}, nil
	}

	pids := make([]int, 0, len(locks))
	for pid := range locks {
		pids = append(pids, pid)
	}

	sort.Ints(pids)

	cwds := map[int]string{}
	if out, err := r.runLsof("-a", "-d", "cwd", "-p", joinPIDs(pids), "-F", "pn"); err == nil {
		cwds = parseLsofPN(out)
	} else {
		r.logger.Warn("lsof cwd", "error", err)
	}

	models := map[int]string{}
	if out, err := r.runPs(pids); err == nil {
		models = parseModels(out)
	} else {
		r.logger.Debug("ps models", "error", err)
	}

	sessions := make([]domain.Session, 0, len(pids))
	for _, pid := range pids {
		sessions = append(sessions, domain.Session{
			Provider: domain.ProviderAntigravity,
			ID:       locks[pid],
			PID:      pid,
			Dir:      cwds[pid],
			Model:    models[pid],
		})
	}

	return sessions, nil
}

// parseLsofPN parses `lsof -F pn` output: a "p<pid>" line starts a process
// block, each "n<name>" line names a file it holds. Returns pid -> last name.
func parseLsofPN(out []byte) map[int]string {
	res := make(map[int]string)
	pid := 0

	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if len(line) < 2 {
			continue
		}

		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(string(line[1:]))
		case 'n':
			if pid > 0 {
				res[pid] = string(line[1:])
			}
		}
	}

	return res
}

// lockHolders keeps only presence lock files and maps pid -> conversation id.
func lockHolders(files map[int]string) map[int]string {
	res := make(map[int]string, len(files))

	for pid, name := range files {
		base := filepath.Base(name)
		if !strings.HasSuffix(base, lockExt) {
			continue
		}

		res[pid] = strings.TrimSuffix(base, lockExt)
	}

	return res
}

// parseModels extracts the --model argument per pid from `ps -o pid=,args=`.
func parseModels(out []byte) map[int]string {
	res := make(map[int]string)

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		for i := 1; i+1 < len(fields); i++ {
			if fields[i] == modelFlag {
				res[pid] = fields[i+1]

				break
			}
		}
	}

	return res
}

func joinPIDs(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}

	return strings.Join(parts, ",")
}
