// Command agent watches Claude Code and Antigravity activity on this machine
// and streams it to the GopherClaude firmware on a Gopher Badge over USB serial.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/agyfs"
	"github.com/dethlex/GopherClaude/internal/infra/anthropic"
	"github.com/dethlex/GopherClaude/internal/infra/badge"
	"github.com/dethlex/GopherClaude/internal/infra/claudefs"
	"github.com/dethlex/GopherClaude/internal/infra/google"
	"github.com/dethlex/GopherClaude/internal/infra/host"
	"github.com/dethlex/GopherClaude/internal/usecase"
)

const (
	defaultInterval = 2 * time.Second
	agyBinaryName   = "agy"
)

// agyInstallDirs are where agy usually ends up ($HOME is expanded).
var agyInstallDirs = []string{"$HOME/.local/bin", "/opt/homebrew/bin", "/usr/local/bin", "$HOME/bin", "$HOME/go/bin"}

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
		agyDir       = flag.String("agy-dir", filepath.Join(home, ".gemini", "antigravity-cli"), "Antigravity CLI data directory")
		eventsFile   = flag.String("events", filepath.Join(home, ".claude-badge", "events.jsonl"), "hook events file")
		dryRun       = flag.Bool("dry-run", false, "log frames instead of writing to the serial port")
		debug        = flag.Bool("debug", false, "verbose logging")
		demo         = flag.Bool("demo", false, "cycle synthetic states to compare the badge's eye patterns")
	)

	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	events := claudefs.NewEventLog(*eventsFile)

	sources := usecase.Sources{
		Claude: usecase.ProviderSources{
			Sessions: claudefs.NewSessionRegistry(filepath.Join(*claudeDir, "sessions"), logger),
			Phases: claudefs.NewResolver(events,
				claudefs.NewTranscriptDir(filepath.Join(*claudeDir, "projects")), logger),
			Plan: anthropic.NewPlanFetcher(logger),
		},
		Usage: claudefs.NewUsageCollector(filepath.Join(*claudeDir, "projects"), time.Local, logger),
		Agy:   agySources(*agyDir, events, logger),
	}

	monitor := usecase.NewMonitor(sources, logger)

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
		"antigravity", sources.Agy != nil,
	)

	ticker := time.NewTicker(*intervalFlag)
	defer ticker.Stop()

	start := time.Now()
	shownScene := -1

	for {
		var snapshot domain.Snapshot

		if *demo {
			name, scene, idx := demoScene(time.Since(start))
			snapshot = scene

			if idx != shownScene {
				shownScene = idx
				logger.Info("demo scene", "module", "main", "scene", name)
			}
		} else {
			snapshot = monitor.Snapshot(time.Now())
		}

		cmds, err := sink.Send(snapshot)
		if err != nil {
			logger.Warn("send frame", "module", "main", "error", err)
		} else {
			logger.Debug("frame sent",
				"module", "main",
				"chats", snapshot.Chats,
				"waiting", snapshot.Waiting,
				"agy_chats", snapshot.Agy.Chats,
				"agy_waiting", snapshot.Agy.Waiting,
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

// agySources wires the Antigravity feeds, or returns nil when agy is not
// installed on this machine (its data directory is absent).
func agySources(dir string, events *claudefs.EventLog, logger *slog.Logger) *usecase.ProviderSources {
	if _, err := os.Stat(dir); err != nil {
		logger.Info("antigravity not detected, skipping", "module", "main", "dir", dir)

		return nil
	}

	agyBinary := findAgyBinary()
	if agyBinary == "" {
		logger.Warn("agy binary not found; set "+google.EnvClientID+"/"+google.EnvClientSecret+" for the Gemini quota", "module", "main")
	}

	return &usecase.ProviderSources{
		Sessions: agyfs.NewSessionRegistry(filepath.Join(dir, "presence"), logger),
		Phases:   claudefs.NewResolver(events, agyfs.NewConversationDir(filepath.Join(dir, "conversations")), logger),
		Plan:     google.NewQuotaFetcher(filepath.Join(dir, "antigravity-oauth-token"), agyBinary, logger),
		Prompts:  agyfs.NewHistory(filepath.Join(dir, "history.jsonl"), time.Local, logger),
	}
}

// findAgyBinary locates the agy executable, whose embedded OAuth client
// credentials the quota fetcher needs. PATH first, then the usual install
// spots: launchd runs the service with a bare PATH that has none of them.
func findAgyBinary() string {
	candidates := make([]string, 0, len(agyInstallDirs)+1)

	if path, err := exec.LookPath(agyBinaryName); err == nil {
		candidates = append(candidates, path)
	}

	home, _ := os.UserHomeDir()
	for _, dir := range agyInstallDirs {
		candidates = append(candidates, filepath.Join(strings.ReplaceAll(dir, "$HOME", home), agyBinaryName))
	}

	for _, path := range candidates {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}

		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			return resolved
		}
	}

	return ""
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
