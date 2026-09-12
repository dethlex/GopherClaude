// Package claudefs reads Claude Code state from the local filesystem:
// the live-session registry and transcript files.
package claudefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Registry statuses that mean the session is not busy. Claude Code writes
// these for cli sessions; claude-desktop ones carry no status at all.
const (
	statusIdle    = "idle"
	statusWaiting = "waiting"
)

// SessionRegistry lists live interactive sessions from ~/.claude/sessions,
// where Claude Code keeps one <pid>.json per running process. Files of dead
// processes linger, so each pid is probed with a 0-signal.
type SessionRegistry struct {
	dir    string
	alive  func(pid int) bool
	logger *slog.Logger
}

func NewSessionRegistry(dir string, logger *slog.Logger) *SessionRegistry {
	return &SessionRegistry{
		dir:    dir,
		alive:  processAlive,
		logger: logger.With("module", "sessions"),
	}
}

type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"`
}

func (r *SessionRegistry) Sessions() ([]domain.Session, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read sessions dir %q: %w", r.dir, err)
	}

	sessions := make([]domain.Session, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(r.dir, entry.Name())

		raw, err := os.ReadFile(path)
		if err != nil {
			r.logger.Warn("read session file", "path", path, "error", err)

			continue
		}

		var sf sessionFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			r.logger.Warn("parse session file", "path", path, "error", err)

			continue
		}

		if sf.Kind != "interactive" || sf.SessionID == "" {
			continue
		}

		if !r.alive(sf.PID) {
			continue
		}

		if _, dup := seen[sf.SessionID]; dup {
			continue
		}

		seen[sf.SessionID] = struct{}{}

		sessions = append(sessions, domain.Session{
			ID:        sf.SessionID,
			PID:       sf.PID,
			Dir:       sf.CWD,
			Idle:      sf.Status == statusIdle || sf.Status == statusWaiting,
			StartedAt: time.UnixMilli(sf.StartedAt),
		})
	}

	return sessions, nil
}

// processAlive probes a pid with signal 0; EPERM still means the process
// exists, it just belongs to someone else.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}
