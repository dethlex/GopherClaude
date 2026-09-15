package main

import (
	"image/color"
	"machine"
	"strconv"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"
)

// Two pages (D-pad left/right); the dashboard cycles its views with D-pad
// up/down. The view list is built from the frame: CLAUDE, one view per extra
// assistant the host monitors (ANTIGRAVITY, CODEX), and ALL once there are
// at least two providers.
//
// CLAUDE view:                         CODEX view (ANTIGRAVITY looks the same):
//
//	✳ CLAUDE                 ◐ [#] ●    ⬡ CODEX                  ◐ [#] ●
//	CHATS 4           WAIT 2            CHATS 1           WAIT 1
//	5-HOUR              44% 3h          5-HOUR                  --
//	[##########............]            [......................]
//	WEEKLY              17% 2d          WEEKLY              17% 6d
//	[#####.................]            [####..................]
//	FABLE   36% 4d   CREDITS      65%   PROMPTS                 3
//	[########....]   [##############]
//	IN 156.4k  OUT 783.5k
//	========== BANNER ==========        ========== BANNER ==========
//
// ALL view: "✳ ✦ ⬡ ALL", summed CHATS/WAIT, then one row per provider with
// its mark and two half-width bars (5H left, WK right).
// Sessions page: the sessions of the view the list was opened from (every
// provider from ALL), seven per page; the cursor walks the whole filtered
// list and wraps, the header counts pages ("SESSIONS 2/3") and the banner
// shows the highlighted session's title and path.
//
// With a single assistant installed the badge drops everything about the
// others: no view to switch to and no mark column in the session list.
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

	barX       = 8
	barW       = screenW - 2*barX
	barH       = 8
	minBarFill = 2 // a non-zero value always shows a sliver
	rowLabelH  = 18
	labelColW  = 90

	// Bar rows of a provider view: three tall rows, bars 6px under their
	// label baseline. The combined view has its own pitch (allRowPitch).
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

	// Header title slot: from the last provider mark to the spinner;
	// headerH covers the Bold9pt ascent and descent around headerBaseline.
	headerH = 24

	// Session-list banner: two Regular9pt lines (title, path) inside the
	// bannerTop..screenH band.
	bannerLine1Baseline = bannerTop + 14
	bannerLine2Baseline = bannerTop + 30

	pageDashboard = 0
	pageSessions  = 1

	// Dashboard views: provider letters plus the combined view, listed per
	// frame (see buildViews). viewAllMark is the pseudo-letter of ALL.
	maxProviders = 1 + maxExtras
	maxViews     = maxProviders + 1
	viewAllMark  = '*'

	// Combined view: one row per provider, two half-width columns per row.
	// Four rows of allRowPitch fit between the counters and the banner.
	allRowPitch  = 36
	allFirstBase = barsTop + 18
	halfGap      = 12
	halfW        = (barW - halfGap) / 2
	rightHalfX   = barX + halfW + halfGap
	halfLabelW   = 40 // mark plus "5H"/"WK" before a column's value area

	// Label slot of a half column on the Claude screen: "CREDIT"-sized, wider
	// than the ALL view's mark-plus-"WK" slot.
	claudeHalfLabelW = 66

	maxBarRows = 2 * maxProviders
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

	// views is the dashboard view list for the current frame (provider
	// letters, then viewAllMark); activeView indexes it. Before the first
	// frame it holds Claude alone.
	views      = [maxViews]byte{provClaude}
	viewCount  = 1
	activeView int

	// Label baselines of a provider view; a bar goes barOffset below.
	rows3 = [3]int16{barsTop + 20, barsTop + 54, barsTop + 88}

	// selRow is the cursor on the session list page: an index into the
	// filtered list (listRows), not a screen row. A opens that session.
	selRow int

	// titleX is where the header title starts, right after the provider
	// marks drawStaticUI painted; renderTitle repaints from there.
	titleX int16

	// listRows indexes f.sessions for the rows the list filter keeps, in
	// frame order; listCount is how many are filled. Rebuilt per render.
	listRows  [maxSessions]int
	listCount int

	// drawnProviders is the assistant set the current chrome was drawn
	// for, so a host that gains or loses one triggers a full repaint.
	drawnProviders providerSet

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
	title    string
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

// providerSet lists the frame's assistant letters, Claude first, zero-padded.
// Arrays compare with ==, so "did the set change" is one cheap check.
type providerSet [maxProviders]byte

func providersOf(f frame) providerSet {
	var set providerSet

	set[0] = provClaude
	for i := 0; i < f.extraCount; i++ {
		set[i+1] = f.extras[i].prov
	}

	return set
}

// viewTitle names the current dashboard view, or the sessions page with its
// page counter once there are multiple pages.
func viewTitle(linked bool) string {
	if activePage != pageSessions {
		return providerTitle(views[activeView])
	}

	pages := listPages()
	if !linked || pages < 2 {
		return "SESSIONS"
	}

	return "SESSIONS " + strconv.Itoa(listPage()+1) + "/" + strconv.Itoa(pages)
}

// providerTitle names a dashboard view by its letter.
func providerTitle(p byte) string {
	switch p {
	case provAgy:
		return "ANTIGRAVITY"
	case provCodex:
		return "CODEX"
	case viewAllMark:
		return "ALL"
	default:
		return "CLAUDE"
	}
}

// headerShowsMark decides which provider marks precede the title: the
// current view's assistant, every assistant on ALL. The session list
// follows the view it was opened from, so the mark doubles as its filter
// indicator.
func headerShowsMark(prov byte) bool {
	return views[activeView] == prov || views[activeView] == viewAllMark
}

// buildViews lists the dashboard views the frame allows: Claude, each extra
// assistant in the host's order, and ALL once there is something to sum. The
// current view survives when its letter is still present; otherwise the
// dashboard parks on Claude.
func buildViews(f frame) {
	current := views[activeView]

	views[0] = provClaude
	viewCount = 1

	for i := 0; i < f.extraCount; i++ {
		views[viewCount] = f.extras[i].prov
		viewCount++
	}

	if f.extraCount > 0 {
		views[viewCount] = viewAllMark
		viewCount++
	}

	activeView = 0

	for i := 0; i < viewCount; i++ {
		if views[i] == current {
			activeView = i
		}
	}
}

// syncProviders keeps the screen honest when the host's assistant set changes
// (the agent restarted after an assistant was installed or removed): it
// rebuilds the view list, parks the dashboard on a view that still exists and
// reports whether the chrome has to be repainted.
func syncProviders(f frame) bool {
	if providersOf(f) == drawnProviders {
		return false
	}

	buildViews(f)

	return true
}

// drawStaticUI paints the parts that only change with the page/view.
func drawStaticUI(f frame) {
	x := int16(headerX)

	for _, prov := range providersOf(f) {
		if prov == 0 || !headerShowsMark(prov) {
			continue
		}

		drawProviderMark(prov, x, headerMarkY)
		x += markSize + markGap
	}

	titleX = x

	if activePage != pageDashboard {
		return
	}

	switch views[activeView] {
	case viewAllMark:
		drawAllLabels(f)
	case provClaude:
		for i, label := range [2]string{"5-HOUR", "WEEKLY"} {
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, rows3[i], label, colLabel)
		}
	default:
		for i, label := range [3]string{"5-HOUR", "WEEKLY", "PROMPTS"} {
			tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, rows3[i], label, colLabel)
		}
	}
}

// renderTitle paints the header title when it changes: on the session list
// it carries the page counter, which moves with the cursor.
func renderTitle(linked bool) {
	title := viewTitle(linked)
	if drawn.valid && drawn.title == title {
		return
	}

	display.FillRectangle(titleX, 0, spinnerX-titleX, headerH, colBg)
	tinyfont.WriteLine(&display, &freemono.Bold9pt7b, titleX, headerBaseline, title, colTitle)

	drawn.title = title
}

// drawAllLabels paints the combined view's chrome: per provider row its mark,
// "5H" over the left column and "WK" over the right one.
func drawAllLabels(f frame) {
	for i, prov := range providersOf(f) {
		if prov == 0 {
			continue
		}

		base := allRowBase(i)

		drawProviderMark(prov, barX, base-markSize)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, markTextX, base, "5H", colLabel)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, rightHalfX, base, "WK", colLabel)
	}
}

// allRowBase is the label baseline of the i-th provider row on ALL.
func allRowBase(i int) int16 {
	return int16(allFirstBase + i*allRowPitch)
}

// workingSessions is how many sessions are busy across every provider: every
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
	drawnProviders = providersOf(f)

	drawStaticUI(f)
	render(f, linked)
}

// switchPage flips between the dashboard and the session list.
func switchPage(f frame, linked bool) {
	activePage = 1 - activePage
	repaintAll(f, linked)
}

// switchView cycles the dashboard through the views the frame allows. With a
// single assistant installed there is nothing to switch to, so the D-pad stays
// inert rather than offering screens with no data behind them.
func switchView(f frame, linked bool, delta int) {
	if viewCount < 2 {
		return
	}

	activeView = (activeView + delta + viewCount) % viewCount
	selRow = 0 // the list filter follows the view, so its cursor restarts
	repaintAll(f, linked)
}

// render repaints every region whose content changed since the last call.
func render(f frame, linked bool) {
	if activePage == pageDashboard {
		renderDashboard(f, linked)
	} else {
		renderSessions(f, linked)
	}

	renderTitle(linked)
	renderBanner(f, linked)

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

// renderBanner paints the bottom band: on the session list the highlighted
// session's title and path, elsewhere the alert message. The details key
// carries a newline no host message can (frames are printable ASCII), so
// the two never collide in the cache.
func renderBanner(f frame, linked bool) {
	if activePage == pageSessions && linked && listCount > 0 {
		s := f.sessions[listRows[selRow]]

		title := s.title
		if title == "" {
			title = s.name
		}

		key := title + "\n" + s.path
		if drawn.valid && drawn.banner == key {
			return
		}

		display.FillRectangle(0, bannerTop, screenW, bannerH, colBanner)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, bannerLine1Baseline, title, colValue)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, bannerLine2Baseline, s.path, colDim)

		drawn.banner = key

		return
	}

	banner, bannerBg, bannerFg := bannerContent(f, linked)
	if !drawn.valid || drawn.banner != banner {
		display.FillRectangle(0, bannerTop, screenW, bannerH, bannerBg)
		writeCentered(&freemono.Bold12pt7b, 0, screenW, bannerBaseline, banner, bannerFg)
		drawn.banner = banner
	}
}

func renderDashboard(f frame, linked bool) {
	switch view := views[activeView]; view {
	case viewAllMark:
		renderAll(f, linked)
	case provClaude:
		renderCounts(linked, f.chats, f.wait)
		renderBarRow(0, rows3[0], f.fivePct, limitValue(f.fivePct, f.fiveRst, f.fiveEta), etaColor(f.fiveEta))
		renderBarRow(1, rows3[1], f.weekPct, limitValue(f.weekPct, f.weekRst, ""), colValue)
		renderClaudeThirdRow(f)
		renderUsage("IN " + fmtTokens(f.tokIn) + "  OUT " + fmtTokens(f.tokOut))
	default:
		renderProvider(f.extraByLetter(view), linked)
	}
}

// renderProvider is the screen of a secondary assistant: counts, both limit
// bars and today's prompt count. Every such assistant shares this layout.
func renderProvider(s providerStats, linked bool) {
	renderCounts(linked, s.chats, s.wait)
	renderBarRow(0, rows3[0], s.fivePct, limitValue(s.fivePct, s.fiveRst, ""), colValue)
	renderBarRow(1, rows3[1], s.weekPct, limitValue(s.weekPct, s.weekRst, ""), colValue)
	renderTextRow(2, rows3[2], strconv.Itoa(s.prompts))
	renderUsage("")
}

// renderAll paints the combined view: summed counts, then per provider a row
// of two half-width bars (5H left, WK right), Claude first. Cache slots are
// row*2 + column.
func renderAll(f frame, linked bool) {
	renderCounts(linked, f.totalChats(), f.totalWait())

	renderHalfRow(0, f.fivePct, allLimitValue(f.fivePct, f.fiveRst, f.fiveEta), etaColor(f.fiveEta),
		f.weekPct, allLimitValue(f.weekPct, f.weekRst, ""))

	for i := 0; i < f.extraCount; i++ {
		e := f.extras[i]

		renderHalfRow(i+1, e.fivePct, allLimitValue(e.fivePct, e.fiveRst, ""), colValue,
			e.weekPct, allLimitValue(e.weekPct, e.weekRst, ""))
	}
}

// allLimitValue is limitValue without the "ETA" word: a half-width column
// has no room for it, so a binding forecast shows as the ETA in place of the
// reset time, in the same red as on the provider's own screen.
func allLimitValue(pct int, reset, eta string) string {
	if eta != "" {
		return limitValue(pct, eta, "")
	}

	return limitValue(pct, reset, "")
}

// renderHalfRow repaints the two columns of one ALL row when they changed.
func renderHalfRow(row int, leftPct int, leftValue string, leftColor color.RGBA, rightPct int, rightValue string) {
	base := allRowBase(row)

	renderHalf(2*row, barX, base, leftPct, leftValue, leftColor)
	renderHalf(2*row+1, rightHalfX, base, rightPct, rightValue, colValue)
}

// renderHalfAt is one half-width limit column with a custom label width: value
// right-aligned to the column's edge, bar under the whole column.
func renderHalfAt(slot int, x, labelBase int16, labelW int16, pct int, value string, valueColor color.RGBA) {
	key := value + "|" + strconv.Itoa(pct)
	if drawn.valid && drawn.rows[slot] == key {
		return
	}

	display.FillRectangle(x+labelW, labelBase-rowLabelH+4, halfW-labelW, rowLabelH, colBg)
	writeRightAligned(&freemono.Regular9pt7b, x+halfW, labelBase, value, valueColor)
	drawBar(x, labelBase+barOffset, halfW, pct)

	drawn.rows[slot] = key
}

// renderHalf is one half-width limit column: value right-aligned to the
// column's edge, bar under the whole column (the left bar runs under the
// mark too, so both bars line up as columns).
func renderHalf(slot int, x, labelBase int16, pct int, value string, valueColor color.RGBA) {
	renderHalfAt(slot, x, labelBase, halfLabelW, pct, value, valueColor)
}

// renderClaudeThirdRow draws CREDITS full width, or, when the plan has
// a model-scoped weekly limit, that limit on the left and CREDITS on the
// right. The labels are part of the row: the layout follows the frame.
func renderClaudeThirdRow(f frame) {
	base := rows3[2]
	key := f.modelLabel + "|" + strconv.Itoa(f.modelPct) + "|" + f.modelRst + "|" + strconv.Itoa(f.credPct) + "|" + f.credTxt
	if drawn.valid && drawn.rows[2] == key {
		return
	}

	display.FillRectangle(0, base-rowLabelH+4, screenW, rowLabelH+barOffset+barH, colBg)

	if f.modelLabel == "" {
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, base, "CREDITS", colLabel)
		drawLimitRow(base, base+barOffset, f.credPct, creditsValue(f.credPct, f.credTxt), colValue)
	} else {
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, base, f.modelLabel, colLabel)
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, rightHalfX, base, "CREDITS", colLabel)
		drawn.rows[3] = "" // the halves are drawn unconditionally below
		drawn.rows[4] = ""
		renderHalfAt(3, barX, base, claudeHalfLabelW, f.modelPct, limitValue(f.modelPct, f.modelRst, ""), colValue)
		renderHalfAt(4, rightHalfX, base, claudeHalfLabelW, f.credPct, pctValue(f.credPct), colValue)
	}

	drawn.rows[2] = key
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
	buildList(f)
	clampSelection()

	rows := sessionLines(f, linked)
	cursor := selRow % sessRowsMax

	for i := 0; i < sessRowsMax; i++ {
		selected := i == cursor && rowSelectable(i)

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

		// Rows without a provider ("no sessions") keep the
		// left margin so the list stays visually aligned.
		textX := int16(barX)
		if drawProviderMark(rows[i].prov, barX, y-markSize) {
			textX = markTextX
		}

		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, textX, y, rows[i].text, rows[i].color)

		drawn.sessions[i] = key
	}
}

// listFilter is the provider the session list shows: the dashboard view the
// user came from, viewAllMark for everyone. With one assistant installed
// the only view is Claude, which is everything anyway.
func listFilter() byte {
	return views[activeView]
}

// buildList collects the frame's sessions the filter keeps, in frame order:
// the host sorts waits first, so they stay on the first page.
func buildList(f frame) {
	filter := listFilter()
	listCount = 0

	for i := range f.sessions {
		if filter != viewAllMark && f.sessions[i].prov != filter {
			continue
		}

		listRows[listCount] = i
		listCount++
	}
}

func listPages() int {
	return (listCount + sessRowsMax - 1) / sessRowsMax
}

// listPage is the page the cursor is on; the rows before it are scrolled
// away rather than summarised.
func listPage() int {
	return selRow / sessRowsMax
}

// rowSelectable reports whether screen row i holds a session on this page.
func rowSelectable(i int) bool {
	return listPage()*sessRowsMax+i < listCount
}

// clampSelection keeps the cursor on a real row as the list grows and
// shrinks.
func clampSelection() {
	if selRow >= listCount {
		selRow = listCount - 1
	}

	if selRow < 0 {
		selRow = 0
	}
}

// moveSelection steps the cursor through the whole filtered list, wrapping
// at both ends: walking past the page's last row opens the next page.
func moveSelection(f frame, delta int) {
	buildList(f)
	clampSelection()

	if listCount == 0 {
		selRow = 0

		return
	}

	selRow = (selRow + delta + listCount) % listCount
}

type sessLine struct {
	text  string
	color color.RGBA
	prov  byte
}

// sessionLines lays the current page out as fixed-width text (the font is
// monospace): "NAME........ P MMMm CTX" — name, phase, minutes, context. The
// provider is drawn as a mark in front of the text, not written into it.
func sessionLines(f frame, linked bool) [sessRowsMax]sessLine {
	var rows [sessRowsMax]sessLine

	if !linked || listCount == 0 {
		rows[0] = sessLine{text: "no sessions", color: colDim}

		return rows
	}

	// The provider column is only worth its width on the combined list
	// with multiple assistants; a filtered list would repeat one mark.
	multi := listFilter() == viewAllMark && f.providerCount() >= 2

	nameChars := sessNameChars
	if !multi {
		nameChars = sessNameWideChars
	}

	start := listPage() * sessRowsMax

	for i := 0; i < sessRowsMax && start+i < listCount; i++ {
		s := f.sessions[listRows[start+i]]

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

		if multi {
			rows[i].prov = s.prov
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

// pctValue is the bare percentage for a half column, where the credits
// text does not fit.
func pctValue(pct int) string {
	if pct == pctUnknown {
		return "--"
	}

	return strconv.Itoa(pct) + "%"
}

// drawLimitRow repaints the value column and the progress bar of one
// full-width row.
func drawLimitRow(labelBase, barY int16, pct int, value string, valueColor color.RGBA) {
	display.FillRectangle(barX+labelColW, labelBase-rowLabelH+4, screenW-barX-labelColW-barX, rowLabelH, colBg)
	writeRightAligned(&freemono.Regular9pt7b, screenW-barX, labelBase, value, valueColor)
	drawBar(barX, barY, barW, pct)
}

// drawBar paints a track of width w and fills it to pct.
func drawBar(x, y, w int16, pct int) {
	display.FillRectangle(x, y, w, barH, colTrack)

	if pct <= 0 {
		return
	}

	fill := int16(int(w) * pct / 100)
	if fill < minBarFill {
		fill = minBarFill
	}

	display.FillRectangle(x, y, fill, barH, barColor(pct))
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
