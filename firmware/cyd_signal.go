//go:build esp32

package main

import (
	"image/color"
	"machine"
	"time"
)

const (
	// The badge's eye colours are tuned for NeoPixels and are dim (values up
	// to ~50); the LED behind a diffuser wants more, hence a gain.
	ledGain = 4

	// The FM8002E pops when its input starts while it is already enabled;
	// give it a moment after enabling before the tone begins.
	ampWarmup = 5 * time.Millisecond
)

var ampEnabled bool

func initLEDs() {
	ledcAttach(ledcChLEDR, ledcTimerLED, pinLEDR)
	ledcAttach(ledcChLEDG, ledcTimerLED, pinLEDG)
	ledcAttach(ledcChLEDB, ledcTimerLED, pinLEDB)
	setEyes(ledOff, ledOff)
}

// setEyes drives the single RGB LED from the badge's two-eye state: the lit
// eye wins, so "alternate red" becomes "blink red" and everything else maps
// one to one. Common anode: full duty is off.
func setEyes(left, right color.RGBA) {
	c := left
	if c == ledOff {
		c = right
	}

	ledcSetDuty(ledcChLEDR, ledcDutyMax-ledLevel(c.R))
	ledcSetDuty(ledcChLEDG, ledcDutyMax-ledLevel(c.G))
	ledcSetDuty(ledcChLEDB, ledcDutyMax-ledLevel(c.B))
}

func ledLevel(v uint8) uint32 {
	level := uint32(v) * ledGain * ledcDutyMax / 255
	if level > ledcDutyMax {
		level = ledcDutyMax
	}

	return level
}

func initBuzzer() {
	pinAmpEn.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinAmpEn.High() // disabled

	ledcAttach(ledcChTone, ledcTimerTone, pinAmp)
}

// tone drives the amplifier input with a square wave; 0 silences it and
// disables the amplifier so the speaker does not hiss between beeps.
func tone(freqHz uint64) {
	if freqHz == 0 {
		ledcSetDuty(ledcChTone, 0)
		pinAmpEn.High()
		ampEnabled = false

		return
	}

	if !ampEnabled {
		pinAmpEn.Low()
		ampEnabled = true
		time.Sleep(ampWarmup)
	}

	ledcSetFrequency(ledcTimerTone, uint32(freqHz))
	ledcSetDuty(ledcChTone, ledcDutyMax/2)
}
