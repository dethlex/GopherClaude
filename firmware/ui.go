package main

import (
	"image/color"
	"machine"
	"strconv"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"
)

// Two pages, toggled with button B (or D-pad left/right):
//
// Dashboard:                          Sessions:
//
//	CLAUDE CONTROL            [#]      CLAUDE CONTROL    SESSIONS [#]
//	CHATS 4           WAIT 2           ClaudeControl  P   5m  412k
//	5-HOUR              44% 3h         rotator        I  12m   73k
//	[##########............]           paperos        W    -  118k
//	WEEKLY              17% 2d         ...up to 7 rows...
//	[#####.................]
//	CREDITS           32.66/50
//	[###############.......]
//	IN 156.4k  OUT 783.5k
//	========== BANNER ==========       ========== BANNER ==========
const (
	screenW = 320
	screenH = 240

	spiFrequency = 32_000_000
	panelHeight  = 320 // physical panel is 240x320; required by the driver

	headerBaseline = 20
	headerX        = 8
	linkDotX       = 300
	linkDotY       = 8
	linkDotSize    = 12

	// Crossed-out speaker icon, shown left of the link dot while muted.
	soundIconX = 270
	soundIconY = 4
	soundIconW = 20
	soundIconH = 16

	// Spinner (work in progress), left of the speaker slot.
	spinnerX       = 244
	spinnerY       = 4
	spinnerW       = 20
	spinnerH       = 16
	spinnerCX      = spinnerX + spinnerW/2
	spinnerCY      = spinnerY + spinnerH/2
	spinnerDotSize = 3
	spinnerSteps   = 8

	countsBaseline = 50
	countsTop      = 28
	countsH        = 26
	chatsX         = 8
	waitX          = 190

	barX      = 8
	barW      = screenW - 2*barX
	barH      = 8
	rowLabelH = 18
	labelColW = 90

	fiveLabelBase = 80
	fiveBarY      = 86
	weekLabelBase = 114
	weekBarY      = 120
	credLabelBase = 148
	credBarY      = 154

	usageBaseline = 186
	usageTop      = 168
	usageH        = 24
	usageX        = 8

	bannerTop      = 204
	bannerH        = screenH - bannerTop
	bannerBaseline = 230

	pctWarn  = 70
	pctAlarm = 90

	// Sessions page geometry.
	sessRowBase   = 48
	sessRowStep   = 22
	sessRowsMax   = 7
	sessNameChars = 14
	sessRowTopPad = 16

	pageDashboard = 0
	pageSessions  = 1
)

var (
	display st7789.Device

	colBg     = color.RGBA{0, 0, 0, 255}
	colTitle  = color.RGBA{0, 180, 220, 255}
	colLabel  = color.RGBA{130, 130, 130, 255}
	colValue  = color.RGBA{255, 255, 255, 255}
	colDim    = color.RGBA{90, 90, 90, 255}
	colGood   = color.RGBA{0, 200, 80, 255}
	colAlert  = color.RGBA{255, 180, 0, 255}
	colBad    = color.RGBA{220, 40, 40, 255}
	colUsage  = color.RGBA{170, 170, 200, 255}
	colBanner = color.RGBA{10, 10, 10, 255}
	colTrack  = color.RGBA{45, 45, 45, 255}
	colBarOK  = color.RGBA{40, 120, 240, 255}
	colSelBg  = color.RGBA{0, 45, 75, 255}

	// Spinner head plus a fading two-dot tail.
	colSpinHead  = color.RGBA{0, 200, 245, 255}
	colSpinTrail = color.RGBA{0, 95, 120, 255}
	colSpinTail  = color.RGBA{0, 45, 60, 255}

	activePage = pageDashboard

	// selRow is the highlighted row on the session list page; A opens it.
	selRow int

	// spinnerPhase is the animation step; spinnerShown is what is currently
	// on screen (-1 = the slot is blank), so the slot is only repainted when
	// it actually changes.
	spinnerPhase int
	spinnerShown = -1

	// Dot offsets around the circle, clockwise from the top (radius ~5).
	spinnerDots = [spinnerSteps][2]int16{
		{0, -5}, {4, -4}, {5, 0}, {4, 4}, {0, 5}, {-4, 4}, {-5, 0}, {-4, -4},
	}
)

// uiCache keeps the last drawn value per region so render only repaints
// regions whose content actually changed (avoids flicker on the slow SPI bus).
type uiCache struct {
	counts   string
	five     string
	week     string
	cred     string
	usage    string
	banner   string
	sessions [sessRowsMax]string
	linked   bool
	sound    bool
	valid    bool
}

var drawn uiCache

func initDisplay() {
	machine.SPI0.Configure(machine.SPIConfig{
		Frequency: spiFrequency,
		Mode:      0,
	})

	display = st7789.New(machine.SPI0,
		machine.TFT_RST,
		machine.TFT_WRX,
		machine.TFT_CS,
		machine.TFT_BACKLIGHT)

	display.Configure(st7789.Config{
		Rotation: st7789.ROTATION_270,
		Height:   panelHeight,
	})

	display.FillScreen(colBg)
}

// setBacklight switches the TFT backlight; the panel content is kept intact
// underneath, so waking up needs no redraw.
func setBacklight(on bool) {
	display.EnableBacklight(on)
}

func drawStaticUI() {
	tinyfont.WriteLine(&display, &freemono.Bold9pt7b, headerX, headerBaseline, "CLAUDE CONTROL", colTitle)

	if activePage == pageDashboard {
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, fiveLabelBase, "5-HOUR", colLabel)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, weekLabelBase, "WEEKLY", colLabel)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, credLabelBase, "CREDITS", colLabel)
	} else {
		// Short label: the spinner slot took the room a longer one would
		// need, and "CLAUDE CONTROL" already reaches x=162.
		writeRightAligned(&freemono.Regular9pt7b, spinnerX-6, headerBaseline, "LIST", colLabel)
	}
}

// workingSessions is how many sessions are busy: every session is either
// working or waiting, so the two counters the badge already receives give the
// answer without an extra protocol field.
func workingSessions(f frame) int {
	n := f.chats - f.wait
	if n < 0 {
		return 0
	}

	return n
}

// advanceSpinner steps the animation; the caller decides how often.
func advanceSpinner() {
	spinnerPhase = (spinnerPhase + 1) % spinnerSteps
}

// renderSpinner paints the progress spinner when work is in flight, and blanks
// the slot otherwise. hidden covers standby / lying flat, where the screen is
// dark and there is nothing to animate.
func renderSpinner(f frame, linked, hidden bool) {
	if !linked || hidden || workingSessions(f) == 0 {
		if spinnerShown != -1 {
			display.FillRectangle(spinnerX, spinnerY, spinnerW, spinnerH, colBg)
			spinnerShown = -1
		}

		return
	}

	if spinnerShown == spinnerPhase {
		return
	}

	for i := 0; i < spinnerSteps; i++ {
		c := colBg

		// i counted backwards from the head: head, then a fading tail.
		switch (spinnerPhase - i + spinnerSteps) % spinnerSteps {
		case 0:
			c = colSpinHead
		case 1:
			c = colSpinTrail
		case 2:
			c = colSpinTail
		}

		display.FillRectangle(
			spinnerCX+spinnerDots[i][0]-spinnerDotSize/2,
			spinnerCY+spinnerDots[i][1]-spinnerDotSize/2,
			spinnerDotSize, spinnerDotSize, c)
	}

	spinnerShown = spinnerPhase
}

// drawSoundIcon paints a crossed-out speaker while the buzzer is disabled,
// or clears the slot when sound is on. Built from rectangles and a pixel
// slash: the 7-bit fonts have no speaker glyph.
func drawSoundIcon(off bool) {
	display.FillRectangle(soundIconX, soundIconY, soundIconW, soundIconH, colBg)

	if !off {
		return
	}

	// Speaker: driver body plus a stepped cone.
	display.FillRectangle(soundIconX+1, soundIconY+5, 4, 6, colAlert)
	display.FillRectangle(soundIconX+5, soundIconY+4, 2, 8, colAlert)
	display.FillRectangle(soundIconX+7, soundIconY+2, 2, 12, colAlert)
	display.FillRectangle(soundIconX+9, soundIconY+1, 2, 14, colAlert)

	// Red slash across, 2px thick.
	for i := int16(0); i < 14; i++ {
		display.SetPixel(soundIconX+3+i, soundIconY+14-i, colBad)
		display.SetPixel(soundIconX+4+i, soundIconY+14-i, colBad)
	}
}

// switchPage flips between the dashboard and the session list and repaints
// everything from scratch.
func switchPage(f frame, linked bool) {
	activePage = 1 - activePage

	display.FillScreen(colBg)

	drawn = uiCache{}
	spinnerShown = -1 // the full wipe cleared the slot too

	drawStaticUI()
	render(f, linked)
}

// render repaints every region whose content changed since the last call.
func render(f frame, linked bool) {
	if activePage == pageDashboard {
		renderDashboard(f, linked)
	} else {
		renderSessions(f, linked)
	}

	banner, bannerBg, bannerFg := bannerContent(f, linked)
	if !drawn.valid || drawn.banner != banner {
		display.FillRectangle(0, bannerTop, screenW, bannerH, bannerBg)
		writeCentered(&freemono.Bold12pt7b, 0, screenW, bannerBaseline, banner, bannerFg)
		drawn.banner = banner
	}

	if !drawn.valid || drawn.linked != linked {
		dotColor := colBad
		if linked {
			dotColor = colGood
		}
		display.FillRectangle(linkDotX, linkDotY, linkDotSize, linkDotSize, dotColor)
		drawn.linked = linked
	}

	if !drawn.valid || drawn.sound != soundOff {
		drawSoundIcon(soundOff)
		drawn.sound = soundOff
	}

	drawn.valid = true
}

func renderDashboard(f frame, linked bool) {
	renderCounts(f, linked)

	fiveColor := colValue
	if f.fiveEta != "" {
		fiveColor = colBad
	}

	five := limitValue(f.fivePct, f.fiveRst, f.fiveEta)
	if !drawn.valid || drawn.five != five {
		drawLimitRow(fiveLabelBase, fiveBarY, f.fivePct, five, fiveColor)
		drawn.five = five
	}

	week := limitValue(f.weekPct, f.weekRst, "")
	if !drawn.valid || drawn.week != week {
		drawLimitRow(weekLabelBase, weekBarY, f.weekPct, week, colValue)
		drawn.week = week
	}

	cred := creditsValue(f.credPct, f.credTxt)
	if !drawn.valid || drawn.cred != cred {
		drawLimitRow(credLabelBase, credBarY, f.credPct, cred, colValue)
		drawn.cred = cred
	}

	usage := "IN " + fmtTokens(f.tokIn) + "  OUT " + fmtTokens(f.tokOut)
	if !drawn.valid || drawn.usage != usage {
		display.FillRectangle(0, usageTop, screenW, usageH, colBg)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, usageX, usageBaseline, usage, colUsage)
		drawn.usage = usage
	}
}

func renderSessions(f frame, linked bool) {
	clampSelection(f)

	rows := sessionLines(f, linked)

	for i := 0; i < sessRowsMax; i++ {
		selected := i == selRow && rowSelectable(f, i)

		// The selection flag is part of the cache key so moving the
		// cursor repaints both the old and the new row.
		key := rows[i].text
		if selected {
			key = ">" + key
		}

		if drawn.valid && drawn.sessions[i] == key {
			continue
		}

		y := int16(sessRowBase + i*sessRowStep)

		bg := colBg
		if selected {
			bg = colSelBg
		}

		display.FillRectangle(0, y-sessRowTopPad, screenW, sessRowStep, bg)

		if rows[i].text != "" {
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, y, rows[i].text, rows[i].color)
		}

		drawn.sessions[i] = key
	}
}

// visibleSessionRows is how many real session rows the list shows (the last
// slot becomes a "+N more" summary when there are too many to fit).
func visibleSessionRows(f frame) int {
	n := len(f.sessions)
	if n > sessRowsMax {
		return sessRowsMax - 1
	}

	return n
}

func rowSelectable(f frame, i int) bool {
	return i < visibleSessionRows(f)
}

// clampSelection keeps the cursor on a real, selectable row as the list grows
// and shrinks.
func clampSelection(f frame) {
	max := visibleSessionRows(f)
	if selRow >= max {
		selRow = max - 1
	}

	if selRow < 0 {
		selRow = 0
	}
}

// moveSelection shifts the cursor by delta within the selectable rows.
func moveSelection(f frame, delta int) {
	selRow += delta
	clampSelection(f)
}

type sessLine struct {
	text  string
	color color.RGBA
}

// sessionLines lays the session rows out as fixed-width text (the font is
// monospace): NAME.......... P MMMm CTX.
func sessionLines(f frame, linked bool) [sessRowsMax]sessLine {
	var rows [sessRowsMax]sessLine

	if !linked || len(f.sessions) == 0 {
		rows[0] = sessLine{text: "no sessions", color: colDim}

		return rows
	}

	visible := len(f.sessions)
	more := 0

	if visible > sessRowsMax {
		visible = sessRowsMax - 1
		more = len(f.sessions) - visible
	}

	for i := 0; i < visible; i++ {
		s := f.sessions[i]

		mins := "-"
		if s.mins > 0 {
			mins = strconv.Itoa(s.mins) + "m"
		}

		ctx := "-"
		if s.ctx > 0 {
			ctx = fmtTokens(s.ctx)
		}

		text := padRight(s.name, sessNameChars) + " " + string(s.phase) + " " +
			padLeft(mins, 4) + " " + padLeft(ctx, 6)

		rows[i] = sessLine{text: text, color: phaseColor(s.phase)}
	}

	if more > 0 {
		rows[sessRowsMax-1] = sessLine{
			text:  "+" + strconv.Itoa(more) + " more",
			color: colDim,
		}
	}

	return rows
}

func phaseColor(phase byte) color.RGBA {
	switch phase {
	case 'P':
		return colBad
	case 'I':
		return colAlert
	default:
		return colValue
	}
}

func renderCounts(f frame, linked bool) {
	chats := "-"
	wait := "-"
	if linked {
		chats = strconv.Itoa(f.chats)
		wait = strconv.Itoa(f.wait)
	}

	key := chats + "|" + wait
	if drawn.valid && drawn.counts == key {
		return
	}

	waitColor := colDim
	if linked && f.wait > 0 {
		waitColor = colBad
	}

	display.FillRectangle(0, countsTop, screenW, countsH, colBg)
	tinyfont.WriteLine(&display, &freemono.Bold12pt7b, chatsX, countsBaseline, "CHATS "+chats, colValue)
	tinyfont.WriteLine(&display, &freemono.Bold12pt7b, waitX, countsBaseline, "WAIT "+wait, waitColor)

	drawn.counts = key
}

// limitValue builds the right column of a limit row: "44% ETA 1.4h" when the
// burn-rate forecast is binding, otherwise "44% 3h", or "--".
func limitValue(pct int, reset, eta string) string {
	if pct == pctUnknown {
		return "--"
	}

	v := strconv.Itoa(pct) + "%"

	if eta != "" {
		return v + " ETA " + eta
	}

	if reset != "" {
		v += " " + reset
	}

	return v
}

func creditsValue(pct int, text string) string {
	if pct == pctUnknown {
		return "--"
	}

	if text != "" {
		return text
	}

	return strconv.Itoa(pct) + "%"
}

// drawLimitRow repaints the value column and the progress bar of one row.
func drawLimitRow(labelBase, barY int16, pct int, value string, valueColor color.RGBA) {
	display.FillRectangle(barX+labelColW, labelBase-rowLabelH+4, screenW-barX-labelColW-barX, rowLabelH, colBg)
	writeRightAligned(&freemono.Regular9pt7b, screenW-barX, labelBase, value, valueColor)

	display.FillRectangle(barX, barY, barW, barH, colTrack)

	if pct <= 0 {
		return
	}

	fill := int16(int(barW) * pct / 100)
	if fill < 2 {
		fill = 2
	}

	display.FillRectangle(barX, barY, fill, barH, barColor(pct))
}

func barColor(pct int) color.RGBA {
	switch {
	case pct >= pctAlarm:
		return colBad
	case pct >= pctWarn:
		return colAlert
	default:
		return colBarOK
	}
}

func bannerContent(f frame, linked bool) (text string, bg, fg color.RGBA) {
	if !linked {
		return "NO LINK", colBanner, colDim
	}

	if f.wait > 0 {
		msg := f.msg
		if msg == "" {
			msg = "ACTION NEEDED"
		}

		return msg, colAlert, colBg
	}

	return "ALL QUIET", colBanner, colGood
}

func padRight(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}

	for len(s) < width {
		s += " "
	}

	return s
}

func padLeft(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}

	for len(s) < width {
		s = " " + s
	}

	return s
}

// writeCentered draws text horizontally centered inside [x, x+w).
func writeCentered(font tinyfont.Fonter, x, w, baseline int16, text string, c color.RGBA) {
	_, outbox := tinyfont.LineWidth(font, text)

	tx := x + (w-int16(outbox))/2
	if tx < x {
		tx = x
	}

	tinyfont.WriteLine(&display, font, tx, baseline, text, c)
}

// writeRightAligned draws text ending at xRight.
func writeRightAligned(font tinyfont.Fonter, xRight, baseline int16, text string, c color.RGBA) {
	_, outbox := tinyfont.LineWidth(font, text)

	tx := xRight - int16(outbox)
	if tx < 0 {
		tx = 0
	}

	tinyfont.WriteLine(&display, font, tx, baseline, text, c)
}
