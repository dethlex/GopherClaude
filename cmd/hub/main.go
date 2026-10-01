// Command hub is the GopherClaude pairing and directory service. It is
// configured from the environment: HUB_LISTEN (default ":8080"), HUB_DATA
// (default "/data/state.json"), HUB_DEBUG ("1" for debug logging).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dethlex/GopherClaude/internal/hub"
)

const (
	defaultListen = ":8080"
	defaultData   = "/data/state.json"

	purgeAge          = 90 * 24 * time.Hour
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := envOr("HUB_LISTEN", defaultListen)
	dataPath := envOr("HUB_DATA", defaultData)
	level := slog.LevelInfo
	if os.Getenv("HUB_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	store, err := hub.Open(dataPath)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	purged, err := store.Purge(time.Now(), purgeAge)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           hub.NewServer(store, logger).Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("hub started", "module", "main", "listen", listen, "data", dataPath, "purged", purged)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("hub stopped", "module", "main")

	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
