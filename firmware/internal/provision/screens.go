package provision

import "claudecontrol/firmware/internal/layout"

// Bottom band of every modal screen, where its buttons live; it is the
// dashboard's banner strip, so a thumb already knows where to go.
const (
	BandTop = layout.BannerTop
	BandH   = layout.ScreenH - layout.BannerTop
)

// BandHit splits the band into `buttons` equal buttons and returns the one
// under x, or -1 outside the band.
func BandHit(x, y int, buttons int) int {
	if y < BandTop || y >= BandTop+BandH || buttons <= 0 || x < 0 || x >= layout.ScreenW {
		return -1
	}

	return x * buttons / layout.ScreenW
}

// ConnectHit is the connecting screen's single CHANGE NETWORK button.
func ConnectHit(x, y int) bool {
	return BandHit(x, y, 1) == 0
}

// Settings menu items, in row order.
var MenuItems = [...]string{"WI-FI", "SOUND", "CALIBRATE", "FACTORY RESET", "BACK"}

const (
	MenuWiFi = iota
	MenuSound
	MenuCalibrate
	MenuFactoryReset
	MenuBack
)

// MenuHit maps a tap to a menu item using the session-list row geometry.
func MenuHit(x, y int) (int, bool) {
	h := layout.HitSessions(x, y)
	if h.Zone != layout.ZoneRow || h.Row >= len(MenuItems) {
		return 0, false
	}

	return h.Row, true
}

// BootStep is what the display does right after calibration.
type BootStep uint8

const (
	StepProvision BootStep = iota // show the network list
	StepConnect                   // join the stored network
)

// MaxBootAttempts is how many boots an unverified network may fail before
// the display stops trusting what was typed and shows the list again. A
// verified network is never abandoned on its own: the router may just be
// down.
const MaxBootAttempts = 3

func NextBootStep(hasNetwork, verified bool, attempts uint8) BootStep {
	if !hasNetwork {
		return StepProvision
	}
	if !verified && attempts >= MaxBootAttempts {
		return StepProvision
	}

	return StepConnect
}
