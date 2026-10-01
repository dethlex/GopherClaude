package usecase

import (
	"errors"
	"log/slog"
	"sort"
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

type fakeSink struct {
	sent   int
	cmds   []domain.Command
	err    error
	closed bool
}

func (s *fakeSink) Send(domain.Snapshot) ([]domain.Command, error) {
	s.sent++
	if s.err != nil {
		return nil, s.err
	}
	return s.cmds, nil
}

func (s *fakeSink) Close() error {
	s.closed = true
	return nil
}

func TestFanoutMergesCommands(t *testing.T) {
	badge := &fakeSink{cmds: []domain.Command{{Name: domain.CommandFocus, Index: domain.NoIndex}}}
	d1 := &fakeSink{cmds: []domain.Command{{Name: domain.CommandFocus, Index: 3}}}
	d2 := &fakeSink{}
	f := NewFanout(slog.Default(), badge)
	f.Add("display:a", d1)
	f.Add("display:b", d2)

	cmds, err := f.Send(domain.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if badge.sent != 1 || d1.sent != 1 || d2.sent != 1 {
		t.Fatalf("sent counts %d %d %d", badge.sent, d1.sent, d2.sent)
	}
	var idx []int
	for _, c := range cmds {
		idx = append(idx, c.Index)
	}
	sort.Ints(idx)
	if len(idx) != 2 || idx[0] != domain.NoIndex || idx[1] != 3 {
		t.Fatalf("commands %+v", cmds)
	}
}

func TestFanoutIsolatesErrors(t *testing.T) {
	badge := &fakeSink{err: errors.New("no usb serial port found")}
	d1 := &fakeSink{cmds: []domain.Command{{Name: domain.CommandFocus, Index: 1}}}
	f := NewFanout(slog.Default(), badge)
	f.Add("display:a", d1)

	cmds, err := f.Send(domain.Snapshot{})
	if err != nil {
		t.Fatalf("a failing member must not fail the fan-out: %v", err)
	}
	if d1.sent != 1 || len(cmds) != 1 {
		t.Fatalf("other members must still be served: sent=%d cmds=%+v", d1.sent, cmds)
	}
}

func TestFanoutAllFailing(t *testing.T) {
	f := NewFanout(slog.Default(), &fakeSink{err: errors.New("boom")})
	if _, err := f.Send(domain.Snapshot{}); err == nil {
		t.Fatal("when every member fails the fan-out reports it")
	}
}

func TestFanoutAddRemoveClose(t *testing.T) {
	badge := &fakeSink{}
	d1 := &fakeSink{}
	f := NewFanout(slog.Default(), badge)
	f.Add("display:a", d1)
	if m := f.Members(); len(m) != 2 {
		t.Fatalf("members %v", m)
	}
	f.Remove("display:a")
	if !d1.closed {
		t.Fatal("removing a member closes it")
	}
	f.Send(domain.Snapshot{})
	if d1.sent != 0 {
		t.Fatal("removed member still served")
	}
	f.Add("display:b", &fakeSink{})
	if err := f.Close(); err != nil || !badge.closed {
		t.Fatalf("close: err=%v badge closed=%v", err, badge.closed)
	}
}
