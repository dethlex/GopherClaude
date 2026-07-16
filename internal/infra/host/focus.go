// Package host carries out actions on the local machine on behalf of the
// badge — currently bringing a Claude Code session's window to the front.
package host

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// Walk at most this far up the process tree looking for the owning GUI
	// app (claude -> shell -> login -> terminal.app is only a few hops).
	maxAncestorHops = 12

	appBundleMarker = ".app/Contents/MacOS/"
	appBundleSuffix = ".app"
)

// WindowFocuser foregrounds the terminal (or Claude Desktop) window that owns
// a session, using `open` so it needs no Automation/TCC permission and works
// from a background launchd agent.
type WindowFocuser struct {
	logger *slog.Logger
}

func NewWindowFocuser(logger *slog.Logger) *WindowFocuser {
	return &WindowFocuser{logger: logger.With("module", "focus")}
}

// procInfo returns a process's executable path and parent pid.
type procInfo func(pid int) (exe string, ppid int)

func (f *WindowFocuser) Focus(target domain.FocusTarget) error {
	bundle := owningAppBundle(target.PID, psProcInfo)
	if bundle == "" {
		return fmt.Errorf("no GUI app owns pid %d (%s)", target.PID, target.Dir)
	}

	if err := exec.Command("open", bundle).Run(); err != nil {
		return fmt.Errorf("open %q: %w", bundle, err)
	}

	f.logger.Info("focused session", "dir", target.Dir, "app", bundle)

	return nil
}

// owningAppBundle walks up from pid and returns the path of the OUTERMOST
// ancestor that lives inside a .app bundle — the user-facing application that
// owns the window. The first .app going up is the wrong one for Claude
// Desktop: it is the embedded claude-code helper bundle
// (…/Application Support/Claude/claude-code/<v>/claude.app), while the real
// window belongs to /Applications/Claude.app higher up the tree. Terminals
// (iTerm, Terminal, …) have a single .app ancestor, so "outermost" is correct
// there too. Empty when none is found (e.g. a detached tmux session).
func owningAppBundle(pid int, info procInfo) string {
	outermost := ""

	cur := pid
	for hop := 0; hop < maxAncestorHops && cur > 1; hop++ {
		exe, parent := info(cur)
		if bundle := bundleFromExecutable(exe); bundle != "" {
			outermost = bundle
		}

		if parent <= 0 || parent == cur {
			break
		}

		cur = parent
	}

	return outermost
}

func bundleFromExecutable(exe string) string {
	idx := strings.Index(exe, appBundleMarker)
	if idx < 0 {
		return ""
	}

	return exe[:idx+len(appBundleSuffix)]
}

// psProcInfo reads a process's executable path and parent pid via `ps`.
func psProcInfo(pid int) (string, int) {
	ppid, err := strconv.Atoi(psField(pid, "ppid="))
	if err != nil {
		ppid = 0
	}

	return psField(pid, "comm="), ppid
}

func psField(pid int, field string) string {
	out, err := exec.Command("ps", "-o", field, "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}
