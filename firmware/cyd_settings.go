//go:build esp32

package main

import (
	"claudecontrol/firmware/internal/gesture"
	"claudecontrol/firmware/internal/provision"
)

// openSettings is the modal menu behind a long press on the title. It
// returns to the caller, which repaints the dashboard; WI-FI may reboot the
// chip instead when the new network refuses the association.
func openSettings(f frame, linked bool) {
	for {
		drawMenu()
		ev, _ := waitGesture(0)
		if ev.Kind != gesture.Tap {
			continue
		}
		item, ok := provision.MenuHit(ev.X, ev.Y)
		if !ok {
			continue
		}

		switch item {
		case provision.MenuWiFi:
			rec := provisionNetwork(settingsRecord)
			for !connectScreen(&rec) {
				rec = provisionNetwork(rec)
			}

			return
		case provision.MenuSound:
			soundOff = !soundOff
			if !soundOff {
				beepSoundOn()
			}
			saveSettings(settings{soundOff: soundOff})
		case provision.MenuCalibrate:
			calibrate()
		case provision.MenuFactoryReset:
			if confirmFactoryReset() {
				factoryReset()
			}
		case provision.MenuBack:
			return
		}
	}
}

func drawMenu() {
	modalClear()
	modalTitle("SETTINGS")
	for i, label := range provision.MenuItems {
		if i == provision.MenuSound {
			if soundOff {
				label += ": OFF"
			} else {
				label += ": ON"
			}
		}
		modalRow(i, label, false)
	}
}

// confirmFactoryReset asks twice: the band shows CANCEL and RESET.
func confirmFactoryReset() bool {
	modalClear()
	modalTitle("FACTORY RESET")
	modalMessage("ERASE ALL SETTINGS?", "network, pairing, calibration")
	modalBand("CANCEL", "RESET")
	for {
		ev, _ := waitGesture(0)
		if ev.Kind != gesture.Tap {
			continue
		}
		switch provision.BandHit(ev.X, ev.Y, 2) {
		case 0:
			return false
		case 1:
			return true
		}
	}
}
