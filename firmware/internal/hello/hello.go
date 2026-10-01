// Package hello holds the display's side of the connection-level lines it
// exchanges with the agent, and the small queue that hands received frames
// from the link goroutine to the core. No hardware, tested on the host.
package hello

import "strings"

const (
	prefix        = "HELLO"
	welcomePrefix = "WELCOME"
	agentIDLen    = 32
	devVersion    = "dev" // when the build carries no version
)

// Line is "HELLO <device-id> <fw-version> <token>\n". The version may not
// contain spaces (the agent splits on them), so they become dashes.
func Line(deviceID, fw, token string) string {
	if fw == "" {
		fw = devVersion
	}
	fw = strings.ReplaceAll(fw, " ", "-")

	return prefix + " " + deviceID + " " + fw + " " + token + "\n"
}

// ParseWelcome extracts the agent id from "WELCOME <agent-id>".
func ParseWelcome(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != welcomePrefix || len(fields[1]) != agentIDLen {
		return "", false
	}

	return fields[1], true
}

// LineQueue is a bounded FIFO of received lines; when full, the oldest line
// goes: a frame the core did not get to is stale anyway.
type LineQueue struct {
	items []string
	head  int
	n     int
}

func NewLineQueue(capacity int) *LineQueue {
	return &LineQueue{items: make([]string, capacity)}
}

// Push appends a line and reports whether an older one was dropped.
func (q *LineQueue) Push(line string) bool {
	dropped := false
	if q.n == len(q.items) {
		q.head = (q.head + 1) % len(q.items)
		q.n--
		dropped = true
	}
	q.items[(q.head+q.n)%len(q.items)] = line
	q.n++

	return dropped
}

// Pop returns the oldest line.
func (q *LineQueue) Pop() (string, bool) {
	if q.n == 0 {
		return "", false
	}
	line := q.items[q.head]
	q.items[q.head] = ""
	q.head = (q.head + 1) % len(q.items)
	q.n--

	return line, true
}

func (q *LineQueue) Len() int {
	return q.n
}
