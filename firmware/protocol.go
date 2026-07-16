package main

import (
	"errors"
	"strconv"
	"strings"
)

// Host -> badge wire format, one frame per line:
//
//	CC3|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<tok_in>|<tok_out>|<msg>|<sessions>\n
//
// Percentages are 0..100, or -1 when the host could not obtain the value.
// <sessions> = name~phase~minutes~ctx_tokens entries joined by ';',
// phase is one of P (permission), I (input), W (working).
const (
	framePrefix = "CC3"
	frameFields = 14

	pctUnknown = -1

	maxSessions      = 8
	sessionSep       = ";"
	sessionFieldSep  = "~"
	sessionFieldsNum = 4
)

type sessionRow struct {
	name  string
	phase byte
	mins  int
	ctx   uint64
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
}

var errBadFrame = errors.New("bad frame")

func parseFrame(line string) (frame, error) {
	parts := strings.SplitN(line, "|", frameFields)
	if len(parts) != frameFields || parts[0] != framePrefix {
		return frame{}, errBadFrame
	}

	var (
		f   frame
		err error
	)

	if f.chats, err = strconv.Atoi(parts[1]); err != nil {
		return frame{}, errBadFrame
	}

	if f.wait, err = strconv.Atoi(parts[2]); err != nil {
		return frame{}, errBadFrame
	}

	if f.fivePct, err = strconv.Atoi(parts[3]); err != nil {
		return frame{}, errBadFrame
	}

	f.fiveRst = parts[4]
	f.fiveEta = parts[5]

	if f.weekPct, err = strconv.Atoi(parts[6]); err != nil {
		return frame{}, errBadFrame
	}

	f.weekRst = parts[7]

	if f.credPct, err = strconv.Atoi(parts[8]); err != nil {
		return frame{}, errBadFrame
	}

	f.credTxt = parts[9]

	if f.tokIn, err = strconv.ParseUint(parts[10], 10, 64); err != nil {
		return frame{}, errBadFrame
	}

	if f.tokOut, err = strconv.ParseUint(parts[11], 10, 64); err != nil {
		return frame{}, errBadFrame
	}

	f.msg = parts[12]
	f.sessions = parseSessions(parts[13])

	return f, nil
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
		if len(fields) != sessionFieldsNum || fields[0] == "" || len(fields[1]) != 1 {
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
		})
	}

	return rows
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
