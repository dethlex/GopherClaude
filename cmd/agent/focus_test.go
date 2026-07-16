package main

import (
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func TestFocusTarget(t *testing.T) {
	banner := &domain.FocusTarget{PID: 1, Dir: "/banner"}
	snap := domain.Snapshot{
		Focus: banner,
		FocusTargets: []domain.FocusTarget{
			{PID: 10, Dir: "/row0"},
			{PID: 11, Dir: "/row1"},
		},
	}

	tests := []struct {
		name  string
		index int
		want  *domain.FocusTarget
	}{
		{"no index falls back to banner", domain.NoIndex, banner},
		{"row 0", 0, &snap.FocusTargets[0]},
		{"row 1", 1, &snap.FocusTargets[1]},
		{"out of range falls back to banner", 9, banner},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := focusTarget(snap, tt.index)
			if got != tt.want {
				t.Errorf("focusTarget(index=%d) = %+v, want %+v", tt.index, got, tt.want)
			}
		})
	}
}

func TestFocusTargetEmpty(t *testing.T) {
	if got := focusTarget(domain.Snapshot{}, 0); got != nil {
		t.Errorf("focusTarget on empty snapshot = %+v, want nil", got)
	}
}
