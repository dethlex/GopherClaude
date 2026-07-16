// Command agent watches Claude Code activity on this machine and streams it
// to the ClaudeControl firmware on a Gopher Badge over USB serial.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/anthropic"
	"github.com/dethlex/GopherClaude/internal/infra/badge"
	"github.com/dethlex/GopherClaude/internal/infra/claudefs"
	"github.com/dethlex/GopherClaude/internal/infra/host"
	"github.com/dethlex/GopherClaude/internal/usecase"
)

const defaultInterval = 2 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var (
		portFlag     = flag.String("port", "auto", "serial port path, or 'auto' to glob /dev/cu.usbmodem*")
		intervalFlag = flag.Duration("interval", defaultInterval, "how often to send a frame")
		claudeDir    = flag.String("claude-dir", filepath.Join(home, ".claude"), "Claude Code data directory")
		eventsFile   = flag.String("events", filepath.Join(home, ".claude-badge", "events.jsonl"), "hook events file")
		dryRun       = flag.Bool("dry-run", false, "log frames instead of writing to the serial port")
		debug        = flag.Bool("debug", false, "verbose logging")
	)

	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	sessions := claudefs.NewSessionRegistry(filepath.Join(*claudeDir, "sessions"), logger)
	usage := claudefs.NewUsageCollector(filepath.Join(*claudeDir, "projects"), time.Local, logger)
	resolver := claudefs.NewResolver(
		claudefs.NewEventLog(*eventsFile),
		claudefs.NewTranscriptDir(filepath.Join(*claudeDir, "projects")),
		logger,
	)
	plan := anthropic.NewPlanFetcher(logger)
	monitor := usecase.NewMonitor(sessions, usage, plan, resolver, logger)

	var sink domain.Sink = badge.NewSerialSink(*portFlag, logger)
	if *dryRun {
		sink = badge.NewDryRunSink(logger)
	}

	focuser := host.NewWindowFocuser(logger)

	defer func() {
		if err := sink.Close(); err != nil {
			logger.Warn("close sink", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("agent started",
		"module", "main",
		"interval", intervalFlag.String(),
		"port", *portFlag,
		"dry_run", *dryRun,
	)

	ticker := time.NewTicker(*intervalFlag)
	defer ticker.Stop()

	for {
		snapshot := monitor.Snapshot(time.Now())

		cmds, err := sink.Send(snapshot)
		if err != nil {
			logger.Warn("send frame", "module", "main", "error", err)
		} else {
			logger.Debug("frame sent",
				"module", "main",
				"chats", snapshot.Chats,
				"waiting", snapshot.Waiting,
				"msg", snapshot.Message,
			)

			handleCommands(cmds, snapshot, focuser, logger)
		}

		select {
		case <-ctx.Done():
			logger.Info("agent stopped", "module", "main")

			return nil
		case <-ticker.C:
		}
	}
}

// handleCommands acts on button presses the badge sent back.
func handleCommands(cmds []domain.Command, snapshot domain.Snapshot, focuser domain.Focuser, logger *slog.Logger) {
	for _, cmd := range cmds {
		if cmd.Name != domain.CommandFocus {
			logger.Debug("unknown badge command", "module", "main", "command", cmd.Name)

			continue
		}

		target := focusTarget(snapshot, cmd.Index)
		if target == nil {
			logger.Debug("focus requested but no target", "module", "main", "index", cmd.Index)

			continue
		}

		if err := focuser.Focus(*target); err != nil {
			logger.Warn("focus session", "module", "main", "error", err)
		}
	}
}

// focusTarget picks the session to open: a specific list row when the badge
// sent an index, otherwise the banner session.
func focusTarget(snapshot domain.Snapshot, index int) *domain.FocusTarget {
	if index >= 0 && index < len(snapshot.FocusTargets) {
		return &snapshot.FocusTargets[index]
	}

	return snapshot.Focus
}
