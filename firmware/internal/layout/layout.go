// Package layout holds the screen geometry the renderer (ui.go) and the
// touch hit-testing share, so a tap lands on exactly the region that was
// drawn. It knows nothing about hardware and is tested on the host.
package layout

const (
	ScreenW = 320
	ScreenH = 240

	// Header: the title strip ends where the spinner slot begins.
	HeaderH  = 24
	SpinnerX = 244

	// Crossed-out speaker icon, right of the spinner, left of the link dot.
	SoundIconX = 270
	SoundIconY = 4
	SoundIconW = 20
	SoundIconH = 16

	// A finger is wider than the 20x16 icon; accept taps this far around it.
	soundIconSlop = 12

	BannerTop = 204

	// Finger calibration on a resistive panel is good to ~15 px, so the
	// title and banner strips grow into the dead space next to them: the
	// title down to the counters row, the banner up to the last list row.
	titleSlop  = (SessRowBase - SessRowTopPad) - HeaderH
	bannerSlop = BannerTop - (SessRowBase - SessRowTopPad + SessRowsMax*SessRowStep)

	// Session list rows: text baseline SessRowBase + i*SessRowStep, each row
	// painted from SessRowTopPad above its baseline, SessRowStep tall.
	SessRowBase   = 48
	SessRowStep   = 22
	SessRowsMax   = 7
	SessRowTopPad = 16
)

// Zone names a tappable region of a page.
type Zone uint8

const (
	ZoneNone Zone = iota
	ZoneTitle
	ZoneSoundIcon
	ZoneBanner
	ZoneRow
)

// Hit is the outcome of a hit test; Row is set for ZoneRow only.
type Hit struct {
	Zone Zone
	Row  int
}

// HitDashboard maps a point on the dashboard page to a zone.
func HitDashboard(x, y int) Hit {
	if h, ok := hitChrome(x, y); ok {
		return h
	}

	return Hit{Zone: ZoneNone}
}

// HitSessions maps a point on the session list page to a zone; rows are
// screen rows 0..SessRowsMax-1, the caller decides whether a row is filled.
func HitSessions(x, y int) Hit {
	if h, ok := hitChrome(x, y); ok {
		return h
	}

	top := SessRowBase - SessRowTopPad
	if y < top || y >= top+SessRowsMax*SessRowStep {
		return Hit{Zone: ZoneNone}
	}

	return Hit{Zone: ZoneRow, Row: (y - top) / SessRowStep}
}

// hitChrome covers the regions both pages share: title, speaker icon, banner.
func hitChrome(x, y int) (Hit, bool) {
	switch {
	case y >= BannerTop-bannerSlop:
		return Hit{Zone: ZoneBanner}, true
	case x >= SoundIconX-soundIconSlop && x < SoundIconX+SoundIconW+soundIconSlop &&
		y < SoundIconY+SoundIconH+soundIconSlop:
		return Hit{Zone: ZoneSoundIcon}, true
	case y < HeaderH+titleSlop && x < SpinnerX:
		return Hit{Zone: ZoneTitle}, true
	}

	return Hit{}, false
}
