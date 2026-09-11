package main

import (
	"image/color"
	"machine"
	"strconv"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"
)

// Two pages (D-pad left/right); the dashboard has three views (D-pad up/down):
//
// CLAUDE view:                         ANTIGRAVITY view:
//
//	✳ CLAUDE                 ◐ [#] ●    ✦ ANTIGRAVITY            ◐ [#] ●
//	CHATS 4           WAIT 2            CHATS 6           WAIT 3
//	5-HOUR              44% 3h          5-HOUR              22% 4h
//	[##########............]            [#####.................]
//	WEEKLY              17% 2d          WEEKLY              18% 6d
//	[#####.................]            [####..................]
//	CREDITS           32.66/50          PROMPTS                 7
//	[###############.......]
//	IN 156.4k  OUT 783.5k
//	========== BANNER ==========        ========== BANNER ==========
//
// ALL view: "✳✦ ALL", summed CHATS/WAIT and four thin bars, each labelled by
// its provider mark (✳ 5H, ✳ WK, ✦ 5H, ✦ WK).
// Sessions page: one row per session across providers, provider mark first.
//
// The frame says which assistants the host monitors. With only one of them
// installed the badge drops everything about the other: the dashboard keeps
// that assistant's screen alone (no ANTIGRAVITY or ALL view to switch to) and
// the session list spends the mark column on longer project names.
const (
	screenW = 320
	screenH = 240

	spiFrequency = 32_000_000
	panelHeight  = 320 // physical panel is 240x320; required by the driver

	headerBaseline = 20
	headerX        = 8

	// Provider marks sit on the text baseline, in the header and in front
	// of the ALL view's bar labels and every session row.
	headerMarkY = headerBaseline - markSize - 1
	markTextX   = headerX + markSize + markGap
	linkDotX    = 300
	linkDotY    = 8
	linkDotSize = 12

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

	// Bar rows: three tall rows for a single provider, four tighter rows
	// for the combined view. Bars sit 6px under their label baseline.
	barsTop   = 60
	barOffset = 6

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
	sessNameChars = 12 // room for the provider column: "✳ name........ P  12m 412k"
	// Without the provider column (a single assistant installed) the name
	// takes the space back.
	sessNameWideChars = 14
	sessRowTopPad     = 16

	pageDashboard = 0
	pageSessions  = 1

	viewClaude = 0
	viewAgy    = 1
	viewAll    = 2
	viewCount  = 3

	// noSoleView marks a host running both assistants: no single view is
	// forced, all three are available.
	noSoleView = -1

	providerBitClaude = 1 << 0
	providerBitAgy    = 1 << 1

	maxBarRows = 4
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
	activeView = viewClaude

	// Label baselines per layout; a bar goes barOffset below its label.
	rows3 = [3]int16{barsTop + 20, barsTop + 54, barsTop + 88}
	rows4 = [4]int16{barsTop + 18, barsTop + 48, barsTop + 78, barsTop + 108}

	// selRow is the highlighted row on the session list page; A opens it.
	selRow int

	// drawnProviders is the assistant set the current chrome was drawn
	// for, so a host that gains or loses one triggers a full repaint.
	drawnProviders byte

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
	rows     [maxBarRows]string
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

// viewTitle names the current dashboard view (or the sessions page).
func viewTitle() string {
	if activePage == pageSessions {
		return "SESSIONS"
	}

	switch activeView {
	case viewAgy:
		return "ANTIGRAVITY"
	case viewAll:
		return "ALL"
	default:
		return "CLAUDE"
	}
}

// headerShowsMark decides which provider marks precede the title: a dashboard
// view is marked by whatever it shows, while the session list marks every
// assistant the host actually monitors.
func headerShowsMark(f frame, prov byte) bool {
	if activePage == pageSessions {
		return providerPresent(f, prov)
	}

	switch activeView {
	case viewAll:
		return true
	case viewAgy:
		return prov == provAgy
	default:
		return prov == provClaude
	}
}

func providerPresent(f frame, prov byte) bool {
	if prov == provAgy {
		return f.hasAgy
	}

	return f.hasClaude
}

// providerBits packs the frame's assistant set so a change is a cheap compare.
func providerBits(f frame) byte {
	var bits byte

	if f.hasClaude {
		bits |= providerBitClaude
	}

	if f.hasAgy {
		bits |= providerBitAgy
	}

	return bits
}

// soleView is the only dashboard view a single-assistant host can show, or
// noSoleView when both are installed and all three views are available.
func soleView(f frame) int {
	if bothProviders(f) {
		return noSoleView
	}

	if f.hasAgy {
		return viewAgy
	}

	return viewClaude
}

// syncProviders keeps the screen honest when the host's assistant set changes
// (the agent restarted after Antigravity was installed or removed): it parks
// the dashboard on a view that still exists and reports whether the chrome has
// to be repainted.
func syncProviders(f frame) bool {
	if providerBits(f) == drawnProviders {
		return false
	}

	if sole := soleView(f); sole != noSoleView {
		activeView = sole
	}

	return true
}

// drawStaticUI paints the parts that only change with the page/view.
func drawStaticUI(f frame) {
	x := int16(headerX)

	for _, prov := range [2]byte{provClaude, provAgy} {
		if !headerShowsMark(f, prov) {
			continue
		}

		drawProviderMark(prov, x, headerMarkY)
		x += markSize + markGap
	}

	tinyfont.WriteLine(&display, &freemono.Bold9pt7b, x, headerBaseline, viewTitle(), colTitle)

	if activePage != pageDashboard {
		return
	}

	switch activeView {
	case viewAll:
		// The bars alternate provider, so each label carries its mark
		// instead of a letter.
		for i, label := range [4]string{"5H", "WK", "5H", "WK"} {
			prov := byte(provClaude)
			if i >= 2 {
				prov = provAgy
			}

			drawProviderMark(prov, barX, rows4[i]-markSize)
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, markTextX, rows4[i], label, colLabel)
		}
	case viewAgy:
		for i, label := range [3]string{"5-HOUR", "WEEKLY", "PROMPTS"} {
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, rows3[i], label, colLabel)
		}
	default:
		for i, label := range [3]string{"5-HOUR", "WEEKLY", "CREDITS"} {
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, rows3[i], label, colLabel)
		}
	}
}

// workingSessions is how many sessions are busy across both providers: every
// session is either working or waiting, so the counters already in the frame
// give the answer without an extra field.
func workingSessions(f frame) int {
	n := f.totalChats() - f.totalWait()
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

// repaintAll wipes the screen and redraws everything for the current page and
// view (labels differ per view, so a partial repaint is not enough).
func repaintAll(f frame, linked bool) {
	display.FillScreen(colBg)

	drawn = uiCache{}
	spinnerShown = -1 // the full wipe cleared the slot too
	drawnProviders = providerBits(f)

	drawStaticUI(f)
	render(f, linked)
}

// switchPage flips between the dashboard and the session list.
func switchPage(f frame, linked bool) {
	activePage = 1 - activePage
	repaintAll(f, linked)
}

// switchView cycles the dashboard between CLAUDE, ANTIGRAVITY and ALL. With a
// single assistant installed there is nothing to switch to, so the D-pad stays
// inert rather than offering screens with no data behind them.
func switchView(f frame, linked bool, delta int) {
	if !bothProviders(f) {
		return
	}

	activeView = (activeView + delta + viewCount) % viewCount
	repaintAll(f, linked)
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
	switch activeView {
	case viewAgy:
		renderCounts(linked, f.agy.chats, f.agy.wait)
		renderBarRow(0, rows3[0], f.agy.fivePct, limitValue(f.agy.fivePct, f.agy.fiveRst, ""), colValue)
		renderBarRow(1, rows3[1], f.agy.weekPct, limitValue(f.agy.weekPct, f.agy.weekRst, ""), colValue)
		renderTextRow(2, rows3[2], strconv.Itoa(f.agyPrompts))
		renderUsage("")
	case viewAll:
		renderCounts(linked, f.totalChats(), f.totalWait())
		renderBarRow(0, rows4[0], f.fivePct, limitValue(f.fivePct, f.fiveRst, f.fiveEta), etaColor(f.fiveEta))
		renderBarRow(1, rows4[1], f.weekPct, limitValue(f.weekPct, f.weekRst, ""), colValue)
		renderBarRow(2, rows4[2], f.agy.fivePct, limitValue(f.agy.fivePct, f.agy.fiveRst, ""), colValue)
		renderBarRow(3, rows4[3], f.agy.weekPct, limitValue(f.agy.weekPct, f.agy.weekRst, ""), colValue)
	default:
		renderCounts(linked, f.chats, f.wait)
		renderBarRow(0, rows3[0], f.fivePct, limitValue(f.fivePct, f.fiveRst, f.fiveEta), etaColor(f.fiveEta))
		renderBarRow(1, rows3[1], f.weekPct, limitValue(f.weekPct, f.weekRst, ""), colValue)
		renderBarRow(2, rows3[2], f.credPct, creditsValue(f.credPct, f.credTxt), colValue)
		renderUsage("IN " + fmtTokens(f.tokIn) + "  OUT " + fmtTokens(f.tokOut))
	}
}

// etaColor turns the 5-hour value red when the burn-rate forecast is binding.
func etaColor(eta string) color.RGBA {
	if eta != "" {
		return colBad
	}

	return colValue
}

// renderBarRow repaints one labelled bar row when its value changed.
func renderBarRow(slot int, labelBase int16, pct int, value string, valueColor color.RGBA) {
	key := value + "|" + strconv.Itoa(pct)
	if drawn.valid && drawn.rows[slot] == key {
		return
	}

	drawLimitRow(labelBase, labelBase+barOffset, pct, value, valueColor)
	drawn.rows[slot] = key
}

// renderTextRow is a bar row without the bar: just a right-aligned value.
func renderTextRow(slot int, labelBase int16, value string) {
	if drawn.valid && drawn.rows[slot] == value {
		return
	}

	display.FillRectangle(barX+labelColW, labelBase-rowLabelH+4, screenW-barX-labelColW-barX, rowLabelH, colBg)
	display.FillRectangle(barX, labelBase+barOffset, barW, barH, colBg)
	writeRightAligned(&freemono.Regular9pt7b, screenW-barX, labelBase, value, colValue)
	drawn.rows[slot] = value
}

func renderUsage(text string) {
	if drawn.valid && drawn.usage == text {
		return
	}

	display.FillRectangle(0, usageTop, screenW, usageH, colBg)

	if text != "" {
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, usageX, usageBaseline, text, colUsage)
	}

	drawn.usage = text
}

func renderSessions(f frame, linked bool) {
	clampSelection(f)

	rows := sessionLines(f, linked)

	for i := 0; i < sessRowsMax; i++ {
		selected := i == selRow && rowSelectable(f, i)

		// The selection flag and the provider are part of the cache key
		// so moving the cursor repaints both the old and the new row,
		// and a row that changes provider repaints its mark.
		key := string(rows[i].prov) + rows[i].text
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

		if rows[i].text == "" {
			drawn.sessions[i] = key

			continue
		}

		// Rows without a provider ("no sessions", "+N more") keep the
		// left margin so the list stays visually aligned.
		textX := int16(barX)
		if drawProviderMark(rows[i].prov, barX, y-markSize) {
			textX = markTextX
		}

		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, textX, y, rows[i].text, rows[i].color)

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
	prov  byte
}

// sessionLines lays the session rows out as fixed-width text (the font is
// monospace): "NAME........ P MMMm CTX" — name, phase, minutes, context. The
// provider is drawn as a mark in front of the text, not written into it.
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

	// The provider column is only worth its width when both assistants are
	// installed; alone, every row would carry the same mark.
	both := bothProviders(f)

	nameChars := sessNameChars
	if !both {
		nameChars = sessNameWideChars
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

		text := padRight(s.name, nameChars) + " " + string(s.phase) + " " +
			padLeft(mins, 4) + " " + padLeft(ctx, 6)

		rows[i] = sessLine{text: text, color: phaseColor(s.phase)}

		if both {
			rows[i].prov = s.prov
		}
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

func renderCounts(linked bool, chats, wait int) {
	chatsTxt := "-"
	waitTxt := "-"
	if linked {
		chatsTxt = strconv.Itoa(chats)
		waitTxt = strconv.Itoa(wait)
	}

	key := chatsTxt + "|" + waitTxt
	if drawn.valid && drawn.counts == key {
		return
	}

	waitColor := colDim
	if linked && wait > 0 {
		waitColor = colBad
	}

	display.FillRectangle(0, countsTop, screenW, countsH, colBg)
	tinyfont.WriteLine(&display, &freemono.Bold12pt7b, chatsX, countsBaseline, "CHATS "+chatsTxt, colValue)
	tinyfont.WriteLine(&display, &freemono.Bold12pt7b, waitX, countsBaseline, "WAIT "+waitTxt, waitColor)

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

	if f.totalWait() > 0 {
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
