//go:build esp32

package main

import (
	"machine"
	"time"

	"tinygo.org/x/drivers/st7789"
)

const (
	// Backlight fades over this long in a few steps; a hard switch on a
	// desk display looks like a fault.
	backlightFade  = 200 * time.Millisecond
	backlightSteps = 10
)

var backlightOn = true

func initDisplay() {
	ledcSetup()
	ledcAttach(ledcChBacklight, ledcTimerBacklight, pinLCDBL)

	machine.SPI2.Configure(lcdSPI)

	// The driver never touches the backlight pin: LEDC owns it.
	display = st7789.New(machine.SPI2, machine.NoPin, pinLCDDC, pinLCDCS, machine.NoPin)
	display.Configure(st7789.Config{
		Rotation: st7789.ROTATION_270,
		Height:   panelHeight,
	})

	display.FillScreen(colBg)
	ledcSetDuty(ledcChBacklight, ledcDutyMax)
}

// setBacklight fades the backlight in or out; the panel content is kept
// intact underneath, so waking up needs no redraw.
func setBacklight(on bool) {
	if on == backlightOn {
		return
	}
	backlightOn = on

	step := backlightFade / backlightSteps
	for i := 1; i <= backlightSteps; i++ {
		level := uint32(ledcDutyMax * i / backlightSteps)
		if !on {
			level = ledcDutyMax - level
		}
		ledcSetDuty(ledcChBacklight, level)
		time.Sleep(step)
	}
}
