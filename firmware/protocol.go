package main

import (
	"errors"
	"strconv"
	"strings"
)

// Host -> badge wire format, one frame per line:
//
//	CC8|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<tok_in>|<tok_out>|<msg>|<sessions>|<extras>|<len>\n
//
// <len> is the byte length of the line before it. The USB receive ring has
// no flow control, so a frame that arrives while the badge repaints loses
// its tail; its head stays in the line buffer and the next frame glues onto
// it. Such a line can still show the right number of separators, and only
// the length gives it away, so a frame whose length disagrees is dropped.
//
// The fixed block is Claude Code. <extras> carries every other installed
// assistant as a repeated group joined by ';':
//
//	<P>~<chats>~<wait>~<5h_pct>~<5h_reset>~<wk_pct>~<wk_reset>~<prompts>   P: A (Antigravity) | X (Codex)
//
// An assistant is present exactly when its group arrives; an empty field
// means Claude alone. Percentages are 0..100, or -1 when the host could not
// obtain the value.
// <sessions> = label~phase~minutes~ctx_tokens~provider~title~path entries
// joined by ';', up to maxSessions of them, waits first; phase is one of P
// (permission), I (input), W (working); provider C, A or X. label is the row
// text, title and path (may be empty) fill the banner for the highlighted
// row; the host already cut them to the badge's widths.
const (
	framePrefix = "CC8"
	frameFields = 16 // the 15 data fields plus the length trailer

	lenField = frameFields - 1

	pctUnknown = -1

	// The list page shows 7 rows; 32 entries are four pages and change,
	// and the most the line buffer is sized for.
	maxSessions      = 32
	sessionSep       = ";"
	sessionFieldSep  = "~"
	sessionFieldsNum = 7

	// The combined view fits four provider rows, so three assistants may
	// come on top of Claude; a group beyond that is dropped, not an error.
	maxExtras      = 3
	extraFieldsNum = 8

	provClaude = 'C'
	provAgy    = 'A'
	provCodex  = 'X'
)

type sessionRow struct {
	name  string
	phase byte
	mins  int
	ctx   uint64
	prov  byte
	title string
	path  string
}

// providerStats is one secondary assistant's block of the frame.
type providerStats struct {
	prov    byte
	chats   int
	wait    int
	fivePct int
	fiveRst string
	weekPct int
	weekRst string
	prompts int
}

type frame struct {
	chats    int
	wait     int
	fivePct  int
	fiveRst  string
	fiveEta  string
	weekPct  int
	weekRst  string
	credPct  int
	credTxt  string
	tokIn    uint64
	tokOut   uint64
	msg      string
	sessions []sessionRow

	// extras are the installed assistants besides Claude in the host's
	// display order; extraCount says how many slots are filled.
	extras     [maxExtras]providerStats
	extraCount int
}

var errBadFrame = errors.New("bad frame")

// totalChats / totalWait span every provider — alerts, eyes and the spinner
// do not care who is waiting.
func (f frame) totalChats() int {
	n := f.chats
	for i := 0; i < f.extraCount; i++ {
		n += f.extras[i].chats
	}

	return n
}

func (f frame) totalWait() int {
	n := f.wait
	for i := 0; i < f.extraCount; i++ {
		n += f.extras[i].wait
	}

	return n
}

// providerCount is how many assistants the frame covers, Claude included.
func (f frame) providerCount() int { return 1 + f.extraCount }

// extraByLetter returns a secondary assistant's block. A letter the frame no
// longer carries yields an empty block with unknown limits: the view list is
// rebuilt from the next frame anyway, so this only bridges one render.
func (f frame) extraByLetter(p byte) providerStats {
	for i := 0; i < f.extraCount; i++ {
		if f.extras[i].prov == p {
			return f.extras[i]
		}
	}

	return providerStats{prov: p, fivePct: pctUnknown, weekPct: pctUnknown}
}

// parseFrame accepts a whole, single frame; when the line fails, the tail
// after the last frame prefix is tried on its own, because a torn frame's
// head glued in front of a whole frame is the common failure and the whole
// frame behind it is still good.
func parseFrame(line string) (frame, error) {
	f, err := parseWhole(line)
	if err == nil {
		return f, nil
	}

	if i := strings.LastIndex(line, framePrefix+"|"); i > 0 {
		return parseWhole(line[i:])
	}

	return frame{}, err
}

// parseWhole rejects anything but exactly one frame: an exact field count (a
// glued pair of frames has twice the separators) and a length trailer that
// matches the bytes actually received.
func parseWhole(line string) (frame, error) {
	parts := strings.Split(line, "|")
	if len(parts) != frameFields || parts[0] != framePrefix {
		return frame{}, errBadFrame
	}

	declared, lenErr := strconv.Atoi(parts[lenField])
	if lenErr != nil || declared != len(line)-len(parts[lenField])-1 {
		return frame{}, errBadFrame
	}

	var f frame

	if !atoiAll(
		[]*int{&f.chats, &f.wait, &f.fivePct, &f.weekPct, &f.credPct},
		[]string{parts[1], parts[2], parts[3], parts[6], parts[8]},
	) {
		return frame{}, errBadFrame
	}

	var err error

	if f.tokIn, err = strconv.ParseUint(parts[10], 10, 64); err != nil {
		return frame{}, errBadFrame
	}

	if f.tokOut, err = strconv.ParseUint(parts[11], 10, 64); err != nil {
		return frame{}, errBadFrame
	}

	f.fiveRst = parts[4]
	f.fiveEta = parts[5]
	f.weekRst = parts[7]
	f.credTxt = parts[9]
	f.msg = parts[12]
	f.sessions = parseSessions(parts[13])
	f.extras, f.extraCount = parseExtras(parts[14])

	return f, nil
}

// atoiAll parses each src into the matching dst; false on the first bad number.
func atoiAll(dst []*int, src []string) bool {
	for i := range dst {
		v, err := strconv.Atoi(src[i])
		if err != nil {
			return false
		}

		*dst[i] = v
	}

	return true
}

// parseExtras reads the secondary-assistant groups. A malformed group (wrong
// field count, unknown letter, non-numeric counter) is skipped and groups
// beyond maxExtras are dropped, so one torn group never discards the frame.
func parseExtras(s string) ([maxExtras]providerStats, int) {
	var (
		extras [maxExtras]providerStats
		count  int
	)

	if s == "" {
		return extras, 0
	}

	for _, group := range strings.Split(s, sessionSep) {
		if count == maxExtras {
			break
		}

		fields := strings.Split(group, sessionFieldSep)
		if len(fields) != extraFieldsNum || len(fields[0]) != 1 || !extraProvider(fields[0][0]) {
			continue
		}

		st := providerStats{prov: fields[0][0], fiveRst: fields[4], weekRst: fields[6]}

		if !atoiAll(
			[]*int{&st.chats, &st.wait, &st.fivePct, &st.weekPct, &st.prompts},
			[]string{fields[1], fields[2], fields[3], fields[5], fields[7]},
		) {
			continue
		}

		extras[count] = st
		count++
	}

	return extras, count
}

// extraProvider reports whether the letter names an assistant this firmware
// has a screen and a mark for; Claude is the fixed block, and a letter from a
// newer host is skipped rather than drawn blank.
func extraProvider(p byte) bool {
	return p == provAgy || p == provCodex
}

// parseSessions tolerates malformed entries (skips them) so a single torn
// row never discards the whole frame.
func parseSessions(s string) []sessionRow {
	if s == "" {
		return nil
	}

	entries := strings.Split(s, sessionSep)

	rows := make([]sessionRow, 0, maxSessions)

	for _, entry := range entries {
		if len(rows) == maxSessions {
			break
		}

		fields := strings.Split(entry, sessionFieldSep)
		if len(fields) != sessionFieldsNum || fields[0] == "" || len(fields[1]) != 1 || len(fields[4]) != 1 {
			continue
		}

		mins, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}

		ctx, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			continue
		}

		rows = append(rows, sessionRow{
			name:  fields[0],
			phase: fields[1][0],
			mins:  mins,
			ctx:   ctx,
			prov:  fields[4][0],
			title: fields[5],
			path:  fields[6],
		})
	}

	return rows
}

// waitingKinds splits the waiting sessions (every provider) by what they wait
// for: a permission dialog (blocks the session until you answer) versus plain
// input (the turn simply ended). The host sorts permission waits first and the
// row list is capped at maxSessions, so a permission wait always makes it into
// the frame.
//
// Older/partial frames may carry no rows at all; a non-zero wait count then
// falls back to "input", the less alarming of the two.
func waitingKinds(f frame) (perm, input int) {
	for _, s := range f.sessions {
		switch s.phase {
		case 'P':
			perm++
		case 'I':
			input++
		}
	}

	if perm == 0 && input == 0 && f.totalWait() > 0 {
		input = f.totalWait()
	}

	return perm, input
}

// fmtTokens renders a token count in a compact human form: 999, 86.5k, 137M.
func fmtTokens(v uint64) string {
	const (
		kilo = 1_000
		mega = 1_000_000
		giga = 1_000_000_000
	)

	switch {
	case v >= giga:
		return scaled(v, giga) + "G"
	case v >= mega:
		return scaled(v, mega) + "M"
	case v >= kilo:
		return scaled(v, kilo) + "k"
	default:
		return strconv.FormatUint(v, 10)
	}
}

// scaled formats v/unit with one decimal below 100 units (86.5) and as an
// integer above (137).
func scaled(v, unit uint64) string {
	const fracDigitDivisor = 10

	whole := v / unit
	if whole >= 100 {
		return strconv.FormatUint(whole, 10)
	}

	frac := (v % unit) * fracDigitDivisor / unit

	return strconv.FormatUint(whole, 10) + "." + strconv.FormatUint(frac, 10)
}
