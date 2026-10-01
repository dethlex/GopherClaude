package badge

import (
	"strings"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Reply lines the firmware sends back, whichever link carries them.
const (
	EchoPrefix  = "ok"   // "ok chats=N wait=M" per frame, "ok busy" from a modal screen
	ErrorPrefix = "err:" // a frame the firmware dropped
)

type ReplyKind uint8

const (
	ReplyEmpty ReplyKind = iota
	ReplyEcho
	ReplyRejected
	ReplyCommand
	ReplyOther
)

// ClassifyReply sorts one reply line; cmd is set for ReplyCommand only.
func ClassifyReply(line string) (ReplyKind, domain.Command) {
	line = strings.TrimSpace(line)

	switch {
	case line == "":
		return ReplyEmpty, domain.Command{}
	case strings.HasPrefix(line, ErrorPrefix):
		return ReplyRejected, domain.Command{}
	case line == EchoPrefix || strings.HasPrefix(line, EchoPrefix+" "):
		return ReplyEcho, domain.Command{}
	}

	if cmd, ok := ParseCommand(line); ok {
		return ReplyCommand, cmd
	}

	return ReplyOther, domain.Command{}
}
