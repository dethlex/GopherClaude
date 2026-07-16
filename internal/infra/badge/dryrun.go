package badge

import (
	"log/slog"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// DryRunSink logs frames instead of sending them — for running the agent
// without the badge attached.
type DryRunSink struct {
	logger *slog.Logger
}

func NewDryRunSink(logger *slog.Logger) *DryRunSink {
	return &DryRunSink{logger: logger.With("module", "dry-run")}
}

func (s *DryRunSink) Send(snapshot domain.Snapshot) ([]domain.Command, error) {
	s.logger.Info("frame", "line", Encode(snapshot, time.Now()))

	return nil, nil
}

func (s *DryRunSink) Close() error {
	return nil
}
