// Package badge encodes snapshots into the badge wire protocol and delivers
// them over the USB CDC serial port.
package badge

import (
	"strconv"
	"strings"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Wire format, one frame per line (firmware/protocol.go is the peer):
//
//	CC6|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<tok_in>|<tok_out>|<msg>|<sessions>|<extras>\n
//
// The fixed block is Claude Code. <extras> lists every other installed
// assistant as a repeated group, groups joined by ';':
//
//	<P>~<chats>~<wait>~<5h_pct>~<5h_reset>~<wk_pct>~<wk_reset>~<prompts>   P: A (Antigravity) | X (Codex)
//
// An assistant is present exactly when its group is sent, so an empty field
// means "Claude alone" and the badge hides every other screen. Percentages
// are 0..100, or -1 when unavailable. Reset/ETA columns are compact
// host-rendered durations ("3h", "45m", "2d") because the badge has no
// clock. The ETA column is non-empty only when the 5-hour limit will run out
// before its reset at the current burn rate.
//
// <sessions> lists up to 8 rows for the badge's session page, all providers
// merged:
//
//	name~phase~minutes~ctx_tokens~provider(;next)*   phase: P|I|W  provider: C|A|X
const (
	framePrefix = "CC6"
	maxMsgLen   = 24
	maxNameLen  = 14

	asciiPrintableMin = 0x20
	asciiPrintableMax = 0x7e

	hoursPerDay      = 24
	resetShownAsDays = 48 * time.Hour

	// sessionSep/fieldSep separate both the session rows and the extras
	// groups: one alphabet keeps the badge parser simple.
	sessionSep = ";"
	fieldSep   = "~"

	// commandPrefix marks a line the badge sends back to the host
	// (e.g. "CMD focus"), distinct from the "ok chats=…" debug echo.
	commandPrefix = "CMD "
)

// ParseCommand extracts a button command from a line the badge sent back,
// e.g. "CMD focus" or "CMD focus 3" (focus session-list row 3). The second
// value is false for anything else (the debug echo, noise).
func ParseCommand(line string) (domain.Command, bool) {
	if !strings.HasPrefix(line, commandPrefix) {
		return domain.Command{}, false
	}

	fields := strings.Fields(line[len(commandPrefix):])
	if len(fields) == 0 {
		return domain.Command{}, false
	}

	cmd := domain.Command{Name: fields[0], Index: domain.NoIndex}

	if len(fields) >= 2 {
		if idx, err := strconv.Atoi(fields[1]); err == nil && idx >= 0 {
			cmd.Index = idx
		}
	}

	return cmd, true
}

// Encode renders a snapshot as a single protocol line (without the trailing
// newline). Text fields are reduced to printable ASCII: the badge fonts have
// no other glyphs, and '|', '~', ';' are separators.
func Encode(s domain.Snapshot, now time.Time) string {
	return framePrefix +
		"|" + strconv.Itoa(s.Chats) +
		"|" + strconv.Itoa(s.Waiting) +
		"|" + strconv.Itoa(s.Plan.FiveHour.Pct) +
		"|" + formatReset(s.Plan.FiveHour, now) +
		"|" + formatETA(s.Plan.FiveHourETA) +
		"|" + strconv.Itoa(s.Plan.Weekly.Pct) +
		"|" + formatReset(s.Plan.Weekly, now) +
		"|" + strconv.Itoa(s.Plan.CreditsPct) +
		"|" + sanitizeText(s.Plan.CreditsText) +
		"|" + strconv.FormatUint(s.Usage.Input, 10) +
		"|" + strconv.FormatUint(s.Usage.Output, 10) +
		"|" + sanitizeText(s.Message) +
		"|" + encodeSessions(s.Sessions) +
		"|" + encodeExtras(s.Extras, now)
}

// encodeExtras renders one group per installed assistant besides Claude, in
// display order; nothing at all when the host monitors Claude alone.
func encodeExtras(extras []domain.ProviderStats, now time.Time) string {
	groups := make([]string, 0, len(extras))

	for _, e := range extras {
		groups = append(groups, strings.Join([]string{
			string(providerLetter(e.Provider)),
			strconv.Itoa(e.Chats),
			strconv.Itoa(e.Waiting),
			strconv.Itoa(e.Plan.FiveHour.Pct),
			formatReset(e.Plan.FiveHour, now),
			strconv.Itoa(e.Plan.Weekly.Pct),
			formatReset(e.Plan.Weekly, now),
			strconv.Itoa(e.Prompts),
		}, fieldSep))
	}

	return strings.Join(groups, sessionSep)
}

func encodeSessions(sessions []domain.SessionBrief) string {
	var b strings.Builder

	for i, sess := range sessions {
		if i > 0 {
			b.WriteString(sessionSep)
		}

		b.WriteString(sanitizeName(sess.Name))
		b.WriteString(fieldSep)
		b.WriteByte(phaseLetter(sess.Phase))
		b.WriteString(fieldSep)
		b.WriteString(strconv.Itoa(sess.Minutes))
		b.WriteString(fieldSep)
		b.WriteString(strconv.FormatUint(sess.CtxTokens, 10))
		b.WriteString(fieldSep)
		b.WriteByte(providerLetter(sess.Provider))
	}

	return b.String()
}

func phaseLetter(p domain.Phase) byte {
	switch p {
	case domain.PhaseWaitingPermission:
		return 'P'
	case domain.PhaseWaitingInput:
		return 'I'
	case domain.PhaseWorking:
		return 'W'
	default:
		return 'W'
	}
}

func providerLetter(p domain.Provider) byte {
	switch p {
	case domain.ProviderAntigravity:
		return 'A'
	case domain.ProviderCodex:
		return 'X'
	case domain.ProviderClaude:
		return 'C'
	default:
		return 'C'
	}
}

// formatETA renders the burn-rate forecast: "1.4h", "45m", or "" when there
// is nothing to warn about.
func formatETA(eta time.Duration) string {
	const tenthsPerHour = 10

	if eta <= 0 {
		return ""
	}

	if eta < time.Hour {
		minutes := int(eta.Minutes())
		if minutes == 0 {
			minutes = 1
		}

		return strconv.Itoa(minutes) + "m"
	}

	tenths := int(eta.Hours() * tenthsPerHour)

	return strconv.Itoa(tenths/tenthsPerHour) + "." + strconv.Itoa(tenths%tenthsPerHour) + "h"
}

// formatReset renders the time until a limit resets the way Claude Desktop
// does ("resets 3h"): days above 48h, then whole hours, then minutes.
func formatReset(l domain.Limit, now time.Time) string {
	if l.Pct == domain.UnknownPct || l.ResetsAt.IsZero() {
		return ""
	}

	until := l.ResetsAt.Sub(now)
	if until <= 0 {
		return "now"
	}

	switch {
	case until >= resetShownAsDays:
		return strconv.Itoa(int(until.Hours())/hoursPerDay) + "d"
	case until >= time.Hour:
		return strconv.Itoa(int(until.Hours())) + "h"
	default:
		minutes := int(until.Minutes())
		if minutes == 0 {
			minutes = 1
		}

		return strconv.Itoa(minutes) + "m"
	}
}

func sanitizeText(msg string) string {
	return sanitize(msg, maxMsgLen)
}

// sanitizeName additionally must not contain the session-list separators.
func sanitizeName(name string) string {
	return sanitize(name, maxNameLen)
}

func sanitize(s string, maxLen int) string {
	var b strings.Builder

	b.Grow(maxLen)

	for _, r := range s {
		if b.Len() >= maxLen {
			break
		}

		switch {
		case r == '|' || r == '~' || r == ';':
			b.WriteByte('/')
		case r >= asciiPrintableMin && r <= asciiPrintableMax:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}

	return b.String()
}
