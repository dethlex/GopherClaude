//go:build esp32

package main

import (
	"image/color"
	"time"

	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"

	"claudecontrol/firmware/internal/gesture"
	"claudecontrol/firmware/internal/layout"
	"claudecontrol/firmware/internal/provision"
)

// Modal screens (first run, settings) block the main loop, so they keep two
// things alive themselves: the watchdog and the serial line. Frames that
// arrive meanwhile are answered with "ok busy" so the agent's echo timer
// does not expire and reopen the port, which would reset the board.
const (
	modalTitleBase = headerBaseline
	modalLineBase  = 60 // first text line under the title
	modalLineStep  = 22
	modalPad       = 8
	modalBandText  = provision.BandTop + 24
	busyEcho       = "ok busy"
)

var (
	colButton     = colSelBg
	colButtonText = colValue
	colKeyText    = colValue
	colKeyDown    = colTitle
)

// waitGesture blocks until the recognizer produces an event, or until the
// timeout (0 = never) passes. It feeds the watchdog and drains the serial
// line every poll.
func waitGesture(timeout time.Duration) (gesture.Event, bool) {
	start := time.Now()
	for {
		feedWatchdog()
		drainSerial()

		now := time.Now()
		if ev, _ := sampleGesture(now); ev.Kind != gesture.None {
			return ev, true
		}
		if timeout > 0 && now.Sub(start) >= timeout {
			return gesture.Event{}, false
		}
		time.Sleep(touchPoll)
	}
}

// drainSerial answers every complete line waiting on the serial port so the
// host keeps hearing from the display while a modal screen is up.
func drainSerial() {
	for {
		if _, ok := pollSerialLine(); !ok {
			return
		}
		println(busyEcho)
	}
}

func modalClear() {
	display.FillScreen(colBg)
}

func modalTitle(text string) {
	display.FillRectangle(0, 0, layout.ScreenW, headerH, colBg)
	tinyfont.WriteLine(&display, &freemono.Bold9pt7b, headerX, modalTitleBase, text, colTitle)
}

// modalLine writes one text line in the body; row 0 sits under the title.
func modalLine(row int, text string, c color.RGBA) {
	base := int16(modalLineBase + row*modalLineStep)
	display.FillRectangle(0, base-rowLabelH+4, layout.ScreenW, modalLineStep, colBg)
	tinyfont.WriteLine(&display, &freemono.Regular9pt7b, modalPad, base, text, c)
}

// modalMessage centres two lines in the body, for status screens.
func modalMessage(line1, line2 string) {
	display.FillRectangle(0, headerH, layout.ScreenW, provision.BandTop-headerH, colBg)
	writeCentered(&freemono.Bold12pt7b, 0, layout.ScreenW, layout.ScreenH/2-8, line1, colValue)
	writeCentered(&freemono.Regular9pt7b, 0, layout.ScreenW, layout.ScreenH/2+20, line2, colLabel)
}

// modalBand draws equal buttons across the bottom band; no labels clears it.
func modalBand(labels ...string) {
	display.FillRectangle(0, provision.BandTop, layout.ScreenW, provision.BandH, colBg)
	if len(labels) == 0 {
		return
	}
	w := int16(layout.ScreenW / len(labels))
	for i, l := range labels {
		x := int16(i) * w
		display.FillRectangle(x+1, provision.BandTop+1, w-2, provision.BandH-2, colButton)
		writeCentered(&freemono.Bold9pt7b, x, w, modalBandText, l, colButtonText)
	}
}

// modalRow draws a list row with the session-list geometry, highlighted when
// selected.
func modalRow(i int, text string, selected bool) {
	y := int16(sessRowBase + i*sessRowStep)
	bg := colBg
	if selected {
		bg = colSelBg
	}
	display.FillRectangle(0, y-sessRowTopPad, layout.ScreenW, sessRowStep, bg)
	if text != "" {
		tinyfont.WriteLine(&display, &freemono.Regular9pt7b, barX, y, text, colValue)
	}
}
