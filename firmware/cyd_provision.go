//go:build esp32

package main

import (
	"net/netip"
	"strconv"
	"time"

	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"

	"claudecontrol/firmware/internal/gesture"
	"claudecontrol/firmware/internal/layout"
	"claudecontrol/firmware/internal/provision"
	"claudecontrol/firmware/internal/store"
)

const (
	// After a failed association the chip reboots (the only cure) once the
	// user has had a moment to read why and to change the network instead.
	rebootDelay = 30 * time.Second

	// How long the "connected" confirmation stays before the dashboard.
	connectedPause = 1500 * time.Millisecond

	// Keyboard feedback: the pressed key inverts for this long.
	keyFlash = 80 * time.Millisecond

	// Text field shows the tail of a long password; Regular9pt is 11 px wide.
	fieldChars = 17

	// A row of the network list: name padded to this many characters, then
	// the signal glyph.
	netNameChars = 18
	barsGlyph    = "|||| "

	bootHeldPollDelay = 50 * time.Millisecond
)

// bootBoard runs the display's first-run flow before the main loop: a
// factory reset if BOOT is held, then the network list until a network is
// stored, then the connecting screen until the network is joined. It
// returns only with the radio up or after the user picked another network
// (which loops back); a refused association reboots instead of returning.
func bootBoard() {
	if bootHeld() {
		factoryReset()
	}

	rec := settingsRecord
	for {
		switch provision.NextBootStep(rec.SSIDLen > 0, rec.WiFiVerified, rec.WiFiAttempts) {
		case provision.StepProvision:
			rec = provisionNetwork(rec)
		case provision.StepConnect:
			if connectScreen(&rec) {
				modalClear()

				return
			}
		}
	}
}

// bootHeld reports BOOT pressed at power-on; the pin is pulled up and the
// button pulls it low.
func bootHeld() bool {
	time.Sleep(bootHeldPollDelay)

	return !pinBoot.Get()
}

// factoryReset writes an empty record (a fresh generation, so it wins over
// whatever is in the other slot) and reboots into calibration.
func factoryReset() {
	modalClear()
	modalMessage("FACTORY RESET", "settings erased")
	writeStore(store.Record{})
	time.Sleep(connectedPause)
	softReset()
}

// provisionNetwork shows the scan result and collects a name and password,
// storing them unverified with the attempt counter cleared. OTHER types the
// name of a hidden network.
func provisionNetwork(rec store.Record) store.Record {
	for {
		modalClear()
		modalTitle("WI-FI NETWORKS")
		modalMessage("SCANNING", "")
		var (
			scan []provision.Network
			err  error
		)
		radioCall(func() { scan, err = radioScan() })
		if err != nil {
			modalMessage("SCAN FAILED", err.Error())
			modalBand("RESCAN")
			waitGesture(0)

			continue
		}

		list := provision.NewNetworkList(scan)
		ssid, ok := networkListScreen(&list)
		if !ok { // OTHER
			ssid, ok = keyboardScreen("NETWORK NAME", "")
			if !ok || ssid == "" {
				continue
			}
		}
		if ssid == "" { // RESCAN
			continue
		}

		password, ok := keyboardScreen("PASSWORD FOR "+ssid, "")
		if !ok {
			continue
		}

		rec.SSIDLen = uint8(copy(rec.SSID[:], ssid))
		rec.PasswordLen = uint8(copy(rec.Password[:], password))
		rec.WiFiVerified = false
		rec.WiFiAttempts = 0
		writeStore(rec)

		return rec
	}
}

// networkListScreen returns the tapped network, "" for RESCAN, or ok=false
// for OTHER.
func networkListScreen(list *provision.NetworkList) (string, bool) {
	drawNetworkList(list)
	for {
		ev, _ := waitGesture(0)
		if ev.Kind != gesture.Tap {
			continue
		}
		action, row := provision.ListHit(ev.X, ev.Y)
		switch action {
		case provision.ListRow:
			rows := list.Rows()
			if row < len(rows) {
				return rows[row].SSID, true
			}
		case provision.ListPageNext:
			list.Next()
			drawNetworkList(list)
		case provision.ListPagePrev:
			list.Prev()
			drawNetworkList(list)
		case provision.ListRescan:
			return "", true
		case provision.ListOther:
			return "", false
		}
	}
}

func drawNetworkList(list *provision.NetworkList) {
	modalClear()
	title := "WI-FI NETWORKS"
	if list.Pages() > 1 {
		title += " " + strconv.Itoa(list.Page()+1) + "/" + strconv.Itoa(list.Pages())
	}
	modalTitle(title)

	rows := list.Rows()
	for i := 0; i < layout.SessRowsMax; i++ {
		text := ""
		if i < len(rows) {
			text = padRight(rows[i].SSID, netNameChars) + barsGlyph[:provision.Bars(rows[i].RSSI)]
		} else if i == 0 && list.Len() == 0 {
			text = "no networks found"
		}
		modalRow(i, text, false)
	}

	// Page column: up and down arrows drawn as text.
	display.FillRectangle(provision.PageColX, sessRowBase-sessRowTopPad, layout.ScreenW-provision.PageColX, layout.SessRowsMax*sessRowStep, colBg)
	if list.Pages() > 1 {
		tinyfont.WriteLine(&display, &freemono.Bold12pt7b, provision.PageColX+12, sessRowBase+sessRowStep, "^", colLabel)
		tinyfont.WriteLine(&display, &freemono.Bold12pt7b, provision.PageColX+12, sessRowBase+5*sessRowStep, "v", colLabel)
	}

	modalBand("RESCAN", "OTHER")
}

// keyboardScreen collects a line of text; ok is false when CANCEL was tapped.
func keyboardScreen(title, initial string) (string, bool) {
	var (
		editor provision.Editor
		page   = provision.PageLower
	)
	editor.Set(initial)

	modalClear()
	modalTitle(title)
	drawField(&editor)
	drawKeyboard(page)

	for {
		ev, _ := waitGesture(0)
		if ev.Kind != gesture.Tap {
			continue
		}

		switch provision.FieldHit(ev.X, ev.Y) {
		case provision.FieldOK:
			return editor.Text(), true
		case provision.FieldCancel:
			return "", false
		}

		key, row, col, ok := provision.KeyAt(page, ev.X, ev.Y)
		if !ok {
			continue
		}

		drawKey(page, row, col, true)
		beepTap()
		time.Sleep(keyFlash)

		if editor.Apply(key) {
			drawField(&editor)
		}
		next := provision.NextPage(page, key)
		if next != page {
			page = next
			drawKeyboard(page)
		} else {
			drawKey(page, row, col, false)
		}
	}
}

func drawField(editor *provision.Editor) {
	display.FillRectangle(0, provision.FieldTop, layout.ScreenW, provision.FieldH, colBg)
	// CANCEL | text | OK
	display.FillRectangle(1, provision.FieldTop+1, provision.CancelW-2, provision.FieldH-2, colButton)
	writeCentered(&freemono.Bold9pt7b, 0, provision.CancelW, provision.FieldTop+24, "X", colButtonText)
	okX := int16(layout.ScreenW - provision.OKW)
	display.FillRectangle(okX+1, provision.FieldTop+1, provision.OKW-2, provision.FieldH-2, colGood)
	writeCentered(&freemono.Bold9pt7b, okX, provision.OKW, provision.FieldTop+24, "OK", colBg)

	text := editor.Tail(fieldChars)
	if len(editor.Text()) > fieldChars {
		text = "<" + text[1:]
	}
	tinyfont.WriteLine(&display, &freemono.Regular9pt7b, provision.CancelW+modalPad, provision.FieldTop+24, text+"_", colValue)
}

func drawKeyboard(page provision.Page) {
	display.FillRectangle(0, provision.GridTop, layout.ScreenW, layout.ScreenH-provision.GridTop, colBg)
	for row := 0; row < provision.Rows; row++ {
		for col := 0; col < provision.Cols; col++ {
			drawKey(page, row, col, false)
		}
	}
}

// drawKey paints one cell; pressed inverts it for the key flash.
func drawKey(page provision.Page, row, col int, pressed bool) {
	k := provision.PageKeys(page)[row][col]
	x := int16(col * provision.KeyW)
	y := int16(provision.GridTop + row*provision.KeyH)
	bg, fg := colBg, colKeyText
	if k.Action != provision.KeyChar {
		bg = colButton
	}
	if pressed {
		bg, fg = colKeyDown, colBg
	}
	display.FillRectangle(x+1, y+1, provision.KeyW-2, provision.KeyH-2, bg)
	if k.Action == provision.KeyNone {
		return
	}
	writeCentered(&freemono.Bold9pt7b, x, provision.KeyW, y+provision.KeyH/2+6, k.Label, fg)
}

// connectScreen joins the stored network. Success marks it verified and
// returns true. Failure counts the boot, shows why, offers CHANGE NETWORK
// for rebootDelay and then reboots; CHANGE NETWORK clears the network and
// returns false; an unverified network that failed MaxBootAttempts boots
// returns false straight away so the list shows again.
func connectScreen(rec *store.Record) bool {
	ssid := string(rec.SSID[:rec.SSIDLen])
	password := string(rec.Password[:rec.PasswordLen])

	modalClear()
	modalTitle("WI-FI")
	modalMessage("CONNECTING", ssid+"  attempt "+strconv.Itoa(int(rec.WiFiAttempts)+1))
	modalBand("CHANGE NETWORK")

	var (
		ip  netip.Addr
		err error
	)
	radioCall(func() { ip, err = radioConnect(ssid, password) })
	if err == nil {
		rec.WiFiVerified = true
		rec.WiFiAttempts = 0
		writeStore(*rec)
		modalMessage("CONNECTED", ip.String())
		modalBand()
		time.Sleep(connectedPause)

		return true
	}

	rec.WiFiAttempts++
	writeStore(*rec)

	if !rec.WiFiVerified && rec.WiFiAttempts >= provision.MaxBootAttempts {
		modalMessage("COULD NOT JOIN "+ssid, "check the password")
		modalBand()
		time.Sleep(rebootDelay / 10)

		return false
	}

	modalMessage("COULD NOT CONNECT", err.Error())
	modalLine(5, "rebooting in "+strconv.Itoa(int(rebootDelay/time.Second))+" s to try again", colLabel)

	// Only a tap on the button cancels the countdown; stray taps are
	// ignored until the deadline.
	deadline := time.Now().Add(rebootDelay)
	for time.Now().Before(deadline) {
		ev, ok := waitGesture(time.Until(deadline))
		if ok && ev.Kind == gesture.Tap && provision.ConnectHit(ev.X, ev.Y) {
			rec.SSIDLen = 0
			rec.PasswordLen = 0
			rec.WiFiVerified = false
			rec.WiFiAttempts = 0
			writeStore(*rec)

			return false
		}
	}

	softReset()

	return false
}
