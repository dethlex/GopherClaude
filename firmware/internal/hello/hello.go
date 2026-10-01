// Package hello holds the display's side of the connection-level lines it
// exchanges with the agent. No hardware, tested on the host.
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
