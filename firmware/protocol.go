package main

import (
	"errors"
	"strconv"
	"strings"
)

// Host -> badge wire format, one frame per line:
//
//	CC5|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<tok_in>|<tok_out>|<msg>|<sessions>|<ag_chats>|<ag_wait>|<ag_5h_pct>|<ag_5h_reset>|<ag_wk_pct>|<ag_wk_reset>|<ag_prompts>|<providers>\n
//
// The first block is Claude Code, the ag_* block is Antigravity. Percentages
// are 0..100, or -1 when the host could not obtain the value.
// <sessions> = name~phase~minutes~ctx_tokens~provider entries joined by ';',
// phase is one of P (permission), I (input), W (working); provider C or A.
// <providers> are the letters of the assistants the host monitors ("C", "CA"):
// an assistant that is not installed gets no screen at all.
const (
	framePrefix = "CC5"
	frameFields = 22

	pctUnknown = -1

	maxSessions      = 8
	sessionSep       = ";"
	sessionFieldSep  = "~"
	sessionFieldsNum = 5

	provClaude = 'C'
	provAgy    = 'A'
)

type sessionRow struct {
	name  string
	phase byte
	mins  int
	ctx   uint64
	prov  byte
}

// providerStats is one assistant's block of the frame.
type providerStats struct {
	chats   int
	wait    int
	fivePct int
	fiveRst string
	weekPct int
	weekRst string
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

	agy        providerStats
	agyPrompts int

	// Which assistants the host monitors; the renderer offers screens and
	// marks only for these.
	hasClaude bool
	hasAgy    bool
}

var errBadFrame = errors.New("bad frame")

// totalChats / totalWait span both providers — alerts, eyes and the spinner
// do not care who is waiting.
func (f frame) totalChats() int { return f.chats + f.agy.chats }
func (f frame) totalWait() int  { return f.wait + f.agy.wait }

func parseFrame(line string) (frame, error) {
	parts := strings.SplitN(line, "|", frameFields)
	if len(parts) != frameFields || parts[0] != framePrefix {
		return frame{}, errBadFrame
	}

	ints := map[int]*int{}
	var f frame

	ints[1], ints[2], ints[3], ints[6], ints[8] = &f.chats, &f.wait, &f.fivePct, &f.weekPct, &f.credPct
	ints[14], ints[15], ints[16], ints[18], ints[20] = &f.agy.chats, &f.agy.wait, &f.agy.fivePct, &f.agy.weekPct, &f.agyPrompts

	for idx, dst := range ints {
		v, err := strconv.Atoi(parts[idx])
		if err != nil {
			return frame{}, errBadFrame
		}

		*dst = v
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
	f.agy.fiveRst = parts[17]
	f.agy.weekRst = parts[19]
	f.hasClaude, f.hasAgy = parseProviders(parts[21])

	return f, nil
}

// parseProviders reads the monitored-assistant letters. An empty or unknown
// set falls back to Claude alone: this badge is useless with no screen, and
// Claude Code is what it is built around.
func parseProviders(s string) (claude, agy bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case provClaude:
			claude = true
		case provAgy:
			agy = true
		}
	}

	if !claude && !agy {
		claude = true
	}

	return claude, agy
}

// bothProviders reports whether the host monitors Claude and Antigravity at
// once — the only case where the badge needs a provider column, the combined
// view, and a way to switch between screens.
func bothProviders(f frame) bool { return f.hasClaude && f.hasAgy }

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
		})
	}

	return rows
}

// waitingKinds splits the waiting sessions (both providers) by what they wait
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
