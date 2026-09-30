//go:build gopher_badge

package main

import (
	"machine"

	"tinygo.org/x/drivers/st7789"
)

const (
	spiFrequency = 32_000_000
	panelHeight  = 320 // physical panel is 240x320; required by the driver
)

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
