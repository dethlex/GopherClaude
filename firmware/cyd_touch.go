//go:build esp32

package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/tinyfont/freemono"

	"claudecontrol/firmware/internal/gesture"
	"claudecontrol/firmware/internal/layout"
	"claudecontrol/firmware/internal/touchcal"
)

// XPT2046 on the LCD's SPI bus: the tinygo driver bit-bangs its own pins,
// which would fight the hardware SPI, so the panel is read over
// machine.SPI2 with the bus clock switched around each read.
const (
	cmdReadX  = 0xD0
	cmdReadY  = 0x90
	touchPoll = 20 * time.Millisecond // 50 Hz

	longPressAfter = 1500 * time.Millisecond
	quickTap       = 300 * time.Millisecond
	swipeMinPx     = 50
	tapSlopPx      = 20 // the panel jitters up to ~18 px under a light finger

	// Each axis is read three times and the two closest readings are
	// averaged: the usual XPT2046 recipe against single-conversion noise.
	touchReads = 3

	// The panel reads garbage while the finger is still landing (the IRQ
	// line drops before the divider settles), so a press only counts this
	// long after contact; a tap shorter than that is noise anyway.
	touchSettle = 40 * time.Millisecond

	// Calibration: samples averaged per cross, and the pause after a cross
	// so the same press does not count for the next one.
	calSamples = 8
	calRelease = 150 * time.Millisecond
	crossArm   = 10
)

var (
	touchRec   = gesture.New(gesture.Config{LongPress: longPressAfter, QuickTap: quickTap, SwipeMin: swipeMinPx, TapSlop: tapSlopPx})
	touchCal   touchcal.Cal
	lastTouch  time.Time
	pressSince time.Time // when the IRQ line went low; zero while released
)

func initInput() {
	pinTouchCS.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinTouchCS.High()
	pinTouchIRQ.Configure(machine.PinConfig{Mode: machine.PinInput})
	pinBoot.Configure(machine.PinConfig{Mode: machine.PinInputPullup})

	touchCal = readStore().Cal
	if !touchCal.Valid {
		calibrate()
	}
}

// touched reports whether the panel is pressed; the IRQ line is authoritative
// and costs nothing, the SPI read happens only while it is low.
func touched() bool {
	return !pinTouchIRQ.Get()
}

func readTouchRaw() touchcal.Raw {
	machine.SPI2.Configure(touchSPI)
	pinTouchCS.Low()
	var xs, ys [touchReads]int
	for i := range xs {
		xs[i] = touchXfer(cmdReadX)
		ys[i] = touchXfer(cmdReadY)
	}
	pinTouchCS.High()
	machine.SPI2.Configure(lcdSPI)

	return touchcal.Raw{X: bestTwo(xs), Y: bestTwo(ys)}
}

// bestTwo averages the two closest of three readings, dropping the outlier.
func bestTwo(v [touchReads]int) int {
	d01, d02, d12 := absInt(v[0]-v[1]), absInt(v[0]-v[2]), absInt(v[1]-v[2])
	switch {
	case d01 <= d02 && d01 <= d12:
		return (v[0] + v[1]) / 2
	case d02 <= d12:
		return (v[0] + v[2]) / 2
	}

	return (v[1] + v[2]) / 2
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}

	return v
}

func touchXfer(cmd byte) int {
	var rx [3]byte
	machine.SPI2.Tx([]byte{cmd, 0, 0}, rx[:])

	return int((uint16(rx[1])<<8 | uint16(rx[2])) >> 3) // 12-bit result
}

// sampleGesture polls the panel once (at most every touchPoll) and feeds the
// recognizer; began reports a press that started with this sample, which
// the standby logic needs.
func sampleGesture(now time.Time) (ev gesture.Event, began bool) {
	if now.Sub(lastTouch) < touchPoll {
		return gesture.Event{}, false
	}
	lastTouch = now

	down := touched()
	if !down {
		pressSince = time.Time{}
	} else if pressSince.IsZero() {
		pressSince = now
	}
	if down && now.Sub(pressSince) < touchSettle {
		down = false
	}

	sample := gesture.Sample{Down: down, At: now}
	if sample.Down {
		sample.X, sample.Y = touchCal.Map(readTouchRaw(), layout.ScreenW, layout.ScreenH)
	}

	wasPressed := touchRec.Pressed()

	return touchRec.Feed(sample), sample.Down && !wasPressed
}

// pollInput samples the panel at 50 Hz, runs the gesture recognizer and maps
// gestures onto the current page. A touch during standby only wakes the
// screen: the press is swallowed so the wake tap never lands on a row.
func pollInput(now time.Time, standby bool) input {
	ev, began := sampleGesture(now)

	if standby && began {
		touchRec.Ignore()

		return input{kind: evWake}
	}

	if ev.Kind == gesture.None {
		return input{}
	}

	if activePage == pageSessions {
		return mapSessionsGesture(ev)
	}

	return mapDashboardGesture(ev)
}

func mapDashboardGesture(ev gesture.Event) input {
	hit := layout.HitDashboard(ev.X, ev.Y)

	switch ev.Kind {
	case gesture.Tap:
		switch hit.Zone {
		case layout.ZoneTitle:
			return input{kind: evViewNext}
		case layout.ZoneBanner:
			return input{kind: evFocus}
		case layout.ZoneSoundIcon:
			return input{kind: evSoundToggle}
		}
	case gesture.LongPress:
		switch hit.Zone {
		case layout.ZoneBanner:
			return input{kind: evMute}
		case layout.ZoneTitle:
			return input{kind: evOpenSettings}
		}
	case gesture.SwipeUp:
		return input{kind: evViewNext}
	case gesture.SwipeDown:
		return input{kind: evViewPrev}
	case gesture.SwipeLeft, gesture.SwipeRight:
		return input{kind: evPageToggle}
	}

	return input{}
}

func mapSessionsGesture(ev gesture.Event) input {
	hit := layout.HitSessions(ev.X, ev.Y)

	switch ev.Kind {
	case gesture.Tap:
		switch hit.Zone {
		case layout.ZoneTitle:
			return input{kind: evPageToggle}
		case layout.ZoneSoundIcon:
			return input{kind: evSoundToggle}
		case layout.ZoneRow:
			row := listPage()*sessRowsMax + hit.Row
			if row >= listCount {
				return input{}
			}
			// First tap highlights (banner shows title and path), the
			// second tap on the same row opens it.
			if row == selRow {
				return input{kind: evFocus}
			}

			return input{kind: evSelectRow, row: row}
		}
	case gesture.LongPress:
		if hit.Zone == layout.ZoneTitle {
			return input{kind: evOpenSettings}
		}
	case gesture.SwipeLeft, gesture.SwipeRight:
		// Horizontal swipes switch pages in both directions, as on the
		// dashboard; the list pages vertically, the way lists scroll.
		return input{kind: evPageToggle}
	case gesture.SwipeUp:
		return input{kind: evListPageNext}
	case gesture.SwipeDown:
		return input{kind: evListPagePrev}
	}

	return input{}
}

// calibrate walks the user through three crosses and stores the result. It
// runs before the main loop, so it draws directly and polls the raw panel.
func calibrate() {
	targets := [3][2]int16{
		{touchcal.RefLeft, touchcal.RefTop},
		{touchcal.RefRight, touchcal.RefTop},
		{touchcal.RefLeft, touchcal.RefBottom},
	}
	var taps [3]touchcal.Raw

	for {
		display.FillScreen(colBg)
		writeCentered(&freemono.Bold12pt7b, 0, screenW, screenH/2, "TOUCH CALIBRATION", colTitle)
		writeCentered(&freemono.Regular9pt7b, 0, screenW, screenH/2+24, "tap each cross", colLabel)

		for i, t := range targets {
			drawCross(t[0], t[1], colAlert)
			taps[i] = waitForTap()
			drawCross(t[0], t[1], colBg)
			time.Sleep(calRelease)
		}

		touchCal = touchcal.FromTaps(taps[0], taps[1], taps[2])
		if touchCal.Valid {
			break
		}

		writeCentered(&freemono.Regular9pt7b, 0, screenW, screenH/2+48, "could not calibrate, again", colBad)
		time.Sleep(2 * time.Second)
	}

	rec := settingsRecord
	rec.Cal = touchCal
	writeStore(rec)
	display.FillScreen(colBg)
}

// waitForTap blocks until the panel is pressed, averages a few raw samples
// and waits for the release.
func waitForTap() touchcal.Raw {
	for !touched() {
		feedWatchdog()
		time.Sleep(touchPoll)
	}
	time.Sleep(touchSettle)

	var sum touchcal.Raw
	for i := 0; i < calSamples; i++ {
		r := readTouchRaw()
		sum.X += r.X
		sum.Y += r.Y
		time.Sleep(touchPoll)
	}

	for touched() {
		feedWatchdog()
		time.Sleep(touchPoll)
	}

	return touchcal.Raw{X: sum.X / calSamples, Y: sum.Y / calSamples}
}

func drawCross(x, y int16, c color.RGBA) {
	display.FillRectangle(x-crossArm, y-1, crossArm*2, 3, c)
	display.FillRectangle(x-1, y-crossArm, 3, crossArm*2, c)
}
