package usecase

import (
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Fanout delivers every frame to all attached sinks and merges their
// commands. The USB badge is a fixed member; displays join and leave as
// they connect. One member's failure is its own problem: the others keep
// receiving, and only when every member fails does Send report an error.
type Fanout struct {
	logger *slog.Logger

	mu      sync.Mutex
	fixed   []domain.Sink
	dynamic map[string]domain.Sink
}

var errNoSinkServed = errors.New("no sink accepted the frame")

func fixedName(i int) string {
	return "fixed:" + strconv.Itoa(i)
}

func NewFanout(logger *slog.Logger, fixed ...domain.Sink) *Fanout {
	return &Fanout{
		logger:  logger.With("module", "fanout"),
		fixed:   fixed,
		dynamic: map[string]domain.Sink{},
	}
}

// Add attaches a named member, replacing (and closing) one with the same
// name.
func (f *Fanout) Add(name string, s domain.Sink) {
	f.mu.Lock()
	old, had := f.dynamic[name]
	f.dynamic[name] = s
	f.mu.Unlock()

	if had {
		if err := old.Close(); err != nil {
			f.logger.Debug("close replaced member", "name", name, "error", err)
		}
	}
}

// Remove detaches and closes a named member; unknown names are ignored.
func (f *Fanout) Remove(name string) {
	f.mu.Lock()
	s, ok := f.dynamic[name]
	delete(f.dynamic, name)
	f.mu.Unlock()

	if ok {
		if err := s.Close(); err != nil {
			f.logger.Debug("close member", "name", name, "error", err)
		}
	}
}

// Members lists the fixed members as "fixed:<i>" and the dynamic ones by
// name, sorted, for logs and tests.
func (f *Fanout) Members() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := make([]string, 0, len(f.fixed)+len(f.dynamic))
	for i := range f.fixed {
		names = append(names, fixedName(i))
	}
	for n := range f.dynamic {
		names = append(names, n)
	}
	sort.Strings(names)

	return names
}

func (f *Fanout) Send(snapshot domain.Snapshot) ([]domain.Command, error) {
	type member struct {
		name string
		sink domain.Sink
	}

	f.mu.Lock()
	members := make([]member, 0, len(f.fixed)+len(f.dynamic))
	for i, s := range f.fixed {
		members = append(members, member{fixedName(i), s})
	}
	for n, s := range f.dynamic {
		members = append(members, member{n, s})
	}
	f.mu.Unlock()

	if len(members) == 0 {
		return nil, errNoSinkServed
	}

	var (
		cmds   []domain.Command
		served int
		last   error
	)
	for _, m := range members {
		c, err := m.sink.Send(snapshot)
		if err != nil {
			last = err
			f.logger.Debug("member failed", "name", m.name, "error", err)

			continue
		}
		served++
		cmds = append(cmds, c...)
	}

	if served == 0 {
		return nil, last
	}

	return cmds, nil
}

func (f *Fanout) Close() error {
	f.mu.Lock()
	members := make([]domain.Sink, 0, len(f.fixed)+len(f.dynamic))
	members = append(members, f.fixed...)
	for _, s := range f.dynamic {
		members = append(members, s)
	}
	f.dynamic = map[string]domain.Sink{}
	f.mu.Unlock()

	var first error
	for _, s := range members {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}

	return first
}
