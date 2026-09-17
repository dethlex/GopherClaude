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
	"path/filepath"
	"strconv"
	"strings"
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

	// The herdr binary name as ps reports it (the server runs by its full
	// Homebrew path, an attached client as a bare name).
	herdrCommand = "herdr"

	// ps prints this as the tty of a process without a controlling
	// terminal: the server, which lives under launchd.
	noTTY = "??"

	psColumns = 4
)

var errNoPane = errors.New("no herdr pane hosts the session")

type (
	// runner executes the herdr CLI; a seam for tests.
	runner func(ctx context.Context, args ...string) ([]byte, error)

	// Focuser focuses the Herdr pane of a session, then hands over to the
	// app-level focuser.
	Focuser struct {
		run runner
		// clients lists the pids of attached herdr clients: Herdr is a TUI,
		// so the window to raise belongs to the terminal hosting one of
		// them, not to anything above the pane's process. A seam for tests.
		clients func(ctx context.Context) []int
		next    domain.Focuser
		logger  *slog.Logger

		// lastUnavailable is the herdr error already warned about, so a
		// broken CLI (protocol mismatch after an update, server down) is
		// reported once per cause rather than on every button press.
		lastUnavailable string
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
		clients: attachedClients,
		next:    next,
		logger:  logger.With("module", "herdr-focus"),
	}
}

// attachedClients finds herdr processes that have a controlling terminal:
// those are attached clients; the server has none.
func attachedClients(ctx context.Context) []int {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,tty=,comm=").Output()
	if err != nil {
		return nil
	}

	return parseClients(out)
}

// parseClients reads `ps -axo pid=,ppid=,tty=,comm=` output.
func parseClients(out []byte) []int {
	var pids []int

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < psColumns {
			continue
		}

		if filepath.Base(fields[3]) != herdrCommand || fields[2] == noTTY {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		pids = append(pids, pid)
	}

	return pids
}

// Focus focuses the session's pane when Herdr hosts it and always lets the
// app focuser run: it raises the Herdr window itself and is the whole
// story for sessions outside Herdr. Herdr trouble is logged, never fatal.
func (f *Focuser) Focus(target domain.FocusTarget) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	focused := false
	appTarget := target

	p, err := f.locate(ctx, target)
	switch {
	case errors.Is(err, errNoPane):
		f.lastUnavailable = ""
		f.logger.Debug("session outside herdr", "dir", target.Dir)
	case err != nil:
		f.warnUnavailable(err)
	default:
		f.lastUnavailable = ""

		if err := f.focusPane(ctx, p); err != nil {
			f.logger.Warn("focus herdr pane", "pane", p.PaneID, "error", err)
		} else {
			focused = true
			f.logger.Info("focused pane", "pane", p.PaneID, "dir", target.Dir)

			// The pane is selected; now the terminal window that shows
			// Herdr must come to the front, and that is the client's.
			if pids := f.clients(ctx); len(pids) > 0 {
				appTarget.PID = pids[0]
			}
		}
	}

	if err := f.next.Focus(appTarget); err != nil {
		if focused {
			f.logger.Debug("app focus after pane focus", "error", err)

			return nil
		}

		return err
	}

	return nil
}

// warnUnavailable reports a herdr failure once per distinct cause.
func (f *Focuser) warnUnavailable(err error) {
	if err.Error() == f.lastUnavailable {
		f.logger.Debug("herdr still unavailable", "error", err)

		return
	}

	f.lastUnavailable = err.Error()
	f.logger.Warn("herdr unavailable, focus raises the app only", "error", err)
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
