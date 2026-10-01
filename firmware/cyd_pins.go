//go:build esp32

package main

import "machine"

// Pin map of the 3.2" ESP32-32E display (lcdwiki), variant E32R32P.
const (
	pinLCDCS    = machine.GPIO15
	pinLCDDC    = machine.GPIO2
	pinLCDBL    = machine.GPIO27
	pinSCK      = machine.GPIO14
	pinMOSI     = machine.GPIO13
	pinMISO     = machine.GPIO12
	pinTouchCS  = machine.GPIO33
	pinTouchIRQ = machine.GPIO36 // low while the panel is pressed
	pinLEDR     = machine.GPIO22 // common anode: low = on
	pinLEDG     = machine.GPIO16
	pinLEDB     = machine.GPIO17
	pinAmp      = machine.GPIO26 // FM8002E input
	pinAmpEn    = machine.GPIO4  // low = amplifier enabled
	pinBattery  = machine.GPIO34
	pinBoot     = machine.GPIO0

	lcdSPIHz    = 40_000_000
	touchSPIHz  = 2_000_000 // XPT2046 tops out at 2.5 MHz
	panelHeight = 320       // physical panel is 240x320; required by the driver
)

var (
	lcdSPI   = machine.SPIConfig{Frequency: lcdSPIHz, SCK: pinSCK, SDO: pinMOSI, SDI: pinMISO}
	touchSPI = machine.SPIConfig{Frequency: touchSPIHz, SCK: pinSCK, SDO: pinMOSI, SDI: pinMISO}
)
