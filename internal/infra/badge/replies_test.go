package badge

import (
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

func TestClassifyReply(t *testing.T) {
	cases := []struct {
		line string
		kind ReplyKind
		idx  int
	}{
		{"", ReplyEmpty, 0},
		{"   ", ReplyEmpty, 0},
		{"ok chats=3 wait=1", ReplyEcho, 0},
		{"ok busy", ReplyEcho, 0},
		{"err: bad frame", ReplyRejected, 0},
		{"CMD focus", ReplyCommand, domain.NoIndex},
		{"CMD focus 4", ReplyCommand, 4},
		{"gopherclaude firmware 1.0", ReplyOther, 0},
	}
	for _, c := range cases {
		kind, cmd := ClassifyReply(c.line)
		if kind != c.kind {
			t.Errorf("%q: kind %v, want %v", c.line, kind, c.kind)
		}
		if kind == ReplyCommand && (cmd.Name != domain.CommandFocus || cmd.Index != c.idx) {
			t.Errorf("%q: command %+v", c.line, cmd)
		}
	}
}
