// Package badge encodes snapshots into the badge wire protocol and delivers
// them over the USB CDC serial port.
package badge

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// Wire format, one frame per line (firmware/protocol.go is the peer):
//
//	CC9|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<model_pct>|<model_reset>|<model_label>|<tok_in>|<tok_out>|<msg>|<sessions>|<extras>|<len>\n
//
// <len> is the byte length of the line before it; the badge drops a frame
// whose length disagrees (torn or glued frames, see Encode).
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
// <sessions> lists up to domain.MaxSessionRows rows for the badge's session
// page, all providers merged, waits first:
//
//	label~phase~minutes~ctx_tokens~provider~title~path(;next)*   phase: P|I|W  provider: C|A|X
//
// label (≤14 bytes) is the row text; title (≤28) and path (≤28) fill the
// banner for the highlighted row. The badge has no ellipsis glyph and
// cannot measure, so the host shortens the path itself ("../src/machine").
const (
	framePrefix = "CC9"
	maxMsgLen   = 24
	maxNameLen  = 14
	// One banner line of the badge's 9pt monospace font.
	maxTitleLen = 28
	maxPathLen  = 28

	// The model label sits in a half-width column's label slot on the
	// badge ("FABLE"); modelLabelField is its index among the frame's
	// fields, which the tests read back.
	maxModelLabelLen = 6
	modelLabelField  = 12

	// A Cyrillic letter transliterates to at most this many ASCII bytes
	// ("щ" → "shch"); sanitizeUnbounded sizes its pass by it.
	maxTranslitExpansion = 4

	// emptyLabel replaces a row label whose text sanitized to nothing (e.g.
	// Cyrillic soft/hard signs only): an empty first field is skipped by the
	// firmware parser and would misalign every later focus index.
	emptyLabel = "?"

	// pathCutMark opens a shortened path. "~" for the home directory is
	// out: it is the field separator.
	pathCutMark = ".."
	pathSep     = "/"

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
	body := framePrefix +
		"|" + strconv.Itoa(s.Chats) +
		"|" + strconv.Itoa(s.Waiting) +
		"|" + strconv.Itoa(s.Plan.FiveHour.Pct) +
		"|" + formatReset(s.Plan.FiveHour, now) +
		"|" + formatETA(s.Plan.FiveHourETA) +
		"|" + strconv.Itoa(s.Plan.Weekly.Pct) +
		"|" + formatReset(s.Plan.Weekly, now) +
		"|" + strconv.Itoa(s.Plan.CreditsPct) +
		"|" + sanitizeText(s.Plan.CreditsText) +
		"|" + strconv.Itoa(s.Plan.Model.Pct) +
		"|" + formatReset(s.Plan.Model, now) +
		"|" + sanitize(strings.ToUpper(s.Plan.ModelLabel), maxModelLabelLen) +
		"|" + strconv.FormatUint(s.Usage.Input, 10) +
		"|" + strconv.FormatUint(s.Usage.Output, 10) +
		"|" + sanitizeText(s.Message) +
		"|" + encodeSessions(s.Sessions) +
		"|" + encodeExtras(s.Extras, now)

	// The trailer is the body's byte length. A USB overrun on the badge
	// tears a frame and leaves its head in the line buffer, where the next
	// frame glues onto it; that glued line kept the right field count, so
	// the length is what lets the badge drop it.
	return body + "|" + strconv.Itoa(len(body))
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

// encodeSessions renders the list rows; the path is sanitized before it is
// shortened so the cut measures the bytes the badge will draw.
func encodeSessions(sessions []domain.SessionBrief) string {
	var b strings.Builder

	for i, sess := range sessions {
		if i > 0 {
			b.WriteString(sessionSep)
		}

		label := sanitizeName(sess.Name)
		if label == "" {
			label = emptyLabel
		}

		b.WriteString(label)
		b.WriteString(fieldSep)
		b.WriteByte(phaseLetter(sess.Phase))
		b.WriteString(fieldSep)
		b.WriteString(strconv.Itoa(sess.Minutes))
		b.WriteString(fieldSep)
		b.WriteString(strconv.FormatUint(sess.CtxTokens, 10))
		b.WriteString(fieldSep)
		b.WriteByte(providerLetter(sess.Provider))
		b.WriteString(fieldSep)
		b.WriteString(sanitize(sess.Title, maxTitleLen))
		b.WriteString(fieldSep)
		b.WriteString(shortenPath(sanitizeUnbounded(sess.Path), maxPathLen))
	}

	return b.String()
}

// shortenPath fits a directory into max bytes: leading components are
// dropped until the rest fits, marked "../"; a last component that is still
// too long is cut from the left.
func shortenPath(path string, max int) string {
	if len(path) <= max {
		return path
	}

	parts := strings.Split(path, pathSep)
	for len(parts) > 1 {
		parts = parts[1:]

		candidate := pathCutMark + pathSep + strings.Join(parts, pathSep)
		if len(candidate) <= max {
			return candidate
		}
	}

	last := parts[0]
	keep := max - len(pathCutMark)

	return pathCutMark + last[len(last)-keep:]
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

var (
	// translit maps Cyrillic to ASCII: the badge fonts are 7-bit and a session
	// renamed in Russian used to show as a row of question marks. Hard and soft
	// signs vanish; a capital letter capitalises its first output letter.
	translit = map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
		'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
		'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
		'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	}
)

// sanitizeUnbounded sanitizes with no limit beyond what transliteration can
// expand the input to; callers that need a cut measure the result.
func sanitizeUnbounded(s string) string {
	return sanitize(s, len(s)*maxTranslitExpansion)
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
			b.WriteString(transliterate(r))
		}
	}

	out := b.String()
	if len(out) > maxLen {
		out = out[:maxLen] // a multi-letter transliteration may overshoot
	}

	return out
}

// transliterate renders one non-ASCII rune: its Latin spelling for Cyrillic,
// "?" for everything else.
func transliterate(r rune) string {
	latin, ok := translit[unicode.ToLower(r)]
	if !ok {
		return "?"
	}

	if unicode.IsUpper(r) && latin != "" {
		return strings.ToUpper(latin[:1]) + latin[1:]
	}

	return latin
}
