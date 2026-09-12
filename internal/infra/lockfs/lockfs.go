// Package lockfs finds live assistant sessions through the lock files their
// processes hold open: agy keeps presence/<conversationId>.lock, Codex
// thread-writer-locks/<threadId>.lock. The files outlive their process, so
// the process table (lsof) is the only truth about who is alive.
package lockfs

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const lockExt = ".lock"

type (
	// Holder is one process holding one lock; ID is the file name without
	// its extension, which is the provider's session id.
	Holder struct {
		PID int
		ID  string
	}

	// Runner executes lsof with the given arguments; a seam for tests.
	Runner func(args ...string) ([]byte, error)

	// BuildFunc turns the live holders into sessions. It receives the Lister
	// so a provider can ask lsof follow-up questions (working directories)
	// for all pids in one batch.
	BuildFunc func(l *Lister, holders []Holder) ([]domain.Session, error)

	// Lister runs lsof over one lock directory.
	Lister struct {
		Dir string
		Run Runner
	}

	lsofEntry struct {
		pid  int
		name string
	}
)

func NewLister(dir string) *Lister {
	return &Lister{Dir: dir, Run: runLsof}
}

// runLsof tolerates exit status 1: lsof returns it when some files have no
// opener, which is not an error here.
func runLsof(args ...string) ([]byte, error) {
	out, _ := exec.Command("lsof", args...).Output()

	return out, nil
}

// Holders lists who holds which lock, sorted by pid then id. Dotfiles (a
// coordination lock), the directory itself and one lock held through several
// descriptors are all folded away.
func (l *Lister) Holders() ([]Holder, error) {
	out, err := l.Run("-F", "pn", "+D", l.Dir)
	if err != nil {
		return nil, fmt.Errorf("lsof %s: %w", l.Dir, err)
	}

	entries := parseLsofPN(out)
	holders := make([]Holder, 0, len(entries))
	seen := make(map[Holder]bool, len(entries))

	for _, e := range entries {
		base := filepath.Base(e.name)
		if strings.HasPrefix(base, ".") || !strings.HasSuffix(base, lockExt) {
			continue
		}

		h := Holder{PID: e.pid, ID: strings.TrimSuffix(base, lockExt)}
		if seen[h] {
			continue
		}

		seen[h] = true
		holders = append(holders, h)
	}

	sort.Slice(holders, func(i, j int) bool {
		if holders[i].PID != holders[j].PID {
			return holders[i].PID < holders[j].PID
		}

		return holders[i].ID < holders[j].ID
	})

	return holders, nil
}

// CWDs returns the working directory of every pid lsof could see.
func (l *Lister) CWDs(pids []int) (map[int]string, error) {
	cwds := make(map[int]string, len(pids))
	if len(pids) == 0 {
		return cwds, nil
	}

	out, err := l.Run("-a", "-d", "cwd", "-p", joinPIDs(pids), "-F", "pn")
	if err != nil {
		return nil, fmt.Errorf("lsof cwd: %w", err)
	}

	for _, e := range parseLsofPN(out) {
		cwds[e.pid] = e.name
	}

	return cwds, nil
}

// parseLsofPN parses `lsof -F pn` output: a "p<pid>" line starts a process
// block, each "n<name>" line names a file it holds; the "f…" descriptor
// lines in between carry nothing this package needs.
func parseLsofPN(out []byte) []lsofEntry {
	var (
		entries []lsofEntry
		pid     int
	)

	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if len(line) < 2 {
			continue
		}

		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(string(line[1:]))
		case 'n':
			if pid > 0 {
				entries = append(entries, lsofEntry{pid: pid, name: string(line[1:])})
			}
		}
	}

	return entries
}

func joinPIDs(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}

	return strings.Join(parts, ",")
}

// Registry caches the sessions a BuildFunc derives from the lock holders:
// lsof costs ~0.6s on a busy machine, far more than a frame tick.
type Registry struct {
	*Lister

	// Now is the clock; a seam for tests.
	Now func() time.Time

	ttl   time.Duration
	build BuildFunc

	cached   []domain.Session
	cachedAt time.Time
}

var _ domain.SessionSource = (*Registry)(nil)

func NewRegistry(dir string, ttl time.Duration, build BuildFunc) *Registry {
	return &Registry{Lister: NewLister(dir), Now: time.Now, ttl: ttl, build: build}
}

func (r *Registry) Sessions() ([]domain.Session, error) {
	now := r.Now()
	if r.cached != nil && now.Sub(r.cachedAt) < r.ttl {
		return r.cached, nil
	}

	holders, err := r.Holders()
	if err != nil {
		return r.cached, err
	}

	sessions, err := r.build(r.Lister, holders)
	if err != nil {
		return r.cached, err
	}

	if sessions == nil {
		sessions = []domain.Session{} // non-nil, so an empty result is cached too
	}

	r.cached = sessions
	r.cachedAt = now

	return sessions, nil
}
