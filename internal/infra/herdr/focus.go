// Package herdr lands the badge's jump button on the exact Herdr pane that
// hosts a session. Herdr's CLI knows each pane's agent session id and
// foreground processes, so a session is found by id first and by pid second;
// the app-level focuser still runs afterwards to raise the window and to
// cover sessions outside Herdr (Claude Desktop, other terminals).
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// EnvBinPath is how Herdr tells processes inside its panes where its
	// CLI lives; the launchd service has no such hint and falls back to
	// PATH and the Homebrew location.
	EnvBinPath = "HERDR_BIN_PATH"

	homebrewBin = "/opt/homebrew/bin/herdr"

	// A button press must not stall the frame loop: herdr answers in
	// milliseconds, anything longer means it is gone.
	callTimeout = 2 * time.Second
)

var errNoPane = errors.New("no herdr pane hosts the session")

type (
	// runner executes the herdr CLI; a seam for tests.
	runner func(ctx context.Context, args ...string) ([]byte, error)

	// Focuser focuses the Herdr pane of a session, then hands over to the
	// app-level focuser.
	Focuser struct {
		run    runner
		next   domain.Focuser
		logger *slog.Logger
	}

	pane struct {
		PaneID  string `json:"pane_id"`
		TabID   string `json:"tab_id"`
		CWD     string `json:"cwd"`
		Session *struct {
			Value string `json:"value"`
		} `json:"agent_session"`
	}

	paneListReply struct {
		Result struct {
			Panes []pane `json:"panes"`
		} `json:"result"`
	}

	processInfoReply struct {
		Result struct {
			Info struct {
				ForegroundPGID int `json:"foreground_process_group_id"`
				Processes      []struct {
					PID int `json:"pid"`
				} `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
)

var _ domain.Focuser = (*Focuser)(nil)

// DefaultBin locates the herdr CLI: the pane environment's hint, then PATH,
// then Homebrew; empty means the feature stays off.
func DefaultBin() string {
	if bin := os.Getenv(EnvBinPath); bin != "" {
		return bin
	}

	if bin, err := exec.LookPath("herdr"); err == nil {
		return bin
	}

	if _, err := os.Stat(homebrewBin); err == nil {
		return homebrewBin
	}

	return ""
}

func NewFocuser(bin string, next domain.Focuser, logger *slog.Logger) *Focuser {
	return &Focuser{
		run: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, bin, args...).Output()
		},
		next:   next,
		logger: logger.With("module", "herdr-focus"),
	}
}

// Focus focuses the session's pane when Herdr hosts it and always lets the
// app focuser run: it raises the Herdr window itself and is the whole
// story for sessions outside Herdr. Herdr trouble is logged, never fatal.
func (f *Focuser) Focus(target domain.FocusTarget) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	focused := false

	p, err := f.locate(ctx, target)
	switch {
	case errors.Is(err, errNoPane):
		f.logger.Debug("session outside herdr", "dir", target.Dir)
	case err != nil:
		f.logger.Debug("herdr unavailable", "error", err)
	default:
		if err := f.focusPane(ctx, p); err != nil {
			f.logger.Warn("focus herdr pane", "pane", p.PaneID, "error", err)
		} else {
			focused = true
			f.logger.Info("focused pane", "pane", p.PaneID, "dir", target.Dir)
		}
	}

	if err := f.next.Focus(target); err != nil {
		if focused {
			f.logger.Debug("app focus after pane focus", "error", err)

			return nil
		}

		return err
	}

	return nil
}

// locate finds the pane by agent session id, else by pid among the panes
// whose working directory is the session's (process-info costs one herdr
// call per pane, the cwd filter keeps that to a few).
func (f *Focuser) locate(ctx context.Context, target domain.FocusTarget) (pane, error) {
	out, err := f.run(ctx, "pane", "list")
	if err != nil {
		return pane{}, fmt.Errorf("pane list: %w", err)
	}

	var reply paneListReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return pane{}, fmt.Errorf("decode pane list: %w", err)
	}

	for _, p := range reply.Result.Panes {
		if target.SessionID != "" && p.Session != nil && p.Session.Value == target.SessionID {
			return p, nil
		}
	}

	for _, p := range reply.Result.Panes {
		if p.CWD != target.Dir {
			continue
		}

		if f.hostsPID(ctx, p.PaneID, target.PID) {
			return p, nil
		}
	}

	return pane{}, errNoPane
}

// hostsPID reports whether the pane's foreground process group is the
// session's process or contains it.
func (f *Focuser) hostsPID(ctx context.Context, paneID string, pid int) bool {
	out, err := f.run(ctx, "pane", "process-info", "--pane", paneID)
	if err != nil {
		f.logger.Debug("pane process-info", "pane", paneID, "error", err)

		return false
	}

	var reply processInfoReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return false
	}

	if reply.Result.Info.ForegroundPGID == pid {
		return true
	}

	for _, proc := range reply.Result.Info.Processes {
		if proc.PID == pid {
			return true
		}
	}

	return false
}

// focusPane prefers the agent focus (it selects the pane itself); a pane
// Herdr does not treat as an agent can only be reached by its tab.
func (f *Focuser) focusPane(ctx context.Context, p pane) error {
	if _, err := f.run(ctx, "agent", "focus", p.PaneID); err == nil {
		return nil
	}

	if _, err := f.run(ctx, "tab", "focus", p.TabID); err != nil {
		return fmt.Errorf("tab focus %s: %w", p.TabID, err)
	}

	return nil
}
