//go:build gopher_badge

package main

import (
	"image/color"
	"machine"

	"tinygo.org/x/drivers/ws2812"
)

var (
	leds      ws2812.Device
	ledColors [2]color.RGBA

	buzzerPWM = machine.PWM7 // machine.SPEAKER (GPIO14) is on PWM slice 7
	buzzerCh  uint8
	buzzerOK  bool
)

func initLEDs() {
	neo := machine.NEOPIXELS
	neo.Configure(machine.PinConfig{Mode: machine.PinOutput})
	leds = ws2812.NewWS2812(neo)
}

// setEyes writes both NeoPixels at once.
func setEyes(left, right color.RGBA) {
	ledColors[0] = left
	ledColors[1] = right
	leds.WriteColors(ledColors[:])
}

func initBuzzer() {
	// The speaker amplifier is gated by SPEAKER_ENABLE; without it the
	// PWM signal never reaches the buzzer.
	machine.SPEAKER_ENABLE.Configure(machine.PinConfig{Mode: machine.PinOutput})
	machine.SPEAKER_ENABLE.High()

	if err := buzzerPWM.Configure(machine.PWMConfig{Period: uint64(1e9) / beepFreqHigh}); err != nil {
		println("buzzer pwm configure:", err.Error())

		return
	}

	ch, err := buzzerPWM.Channel(machine.SPEAKER)
	if err != nil {
		println("buzzer pwm channel:", err.Error())

		return
	}

	buzzerCh = ch
	buzzerOK = true
}

func tone(freqHz uint64) {
	if !buzzerOK {
		return
	}

	if freqHz == 0 {
		buzzerPWM.Set(buzzerCh, 0)

		return
	}

	buzzerPWM.SetPeriod(uint64(1e9) / freqHz)
	buzzerPWM.Set(buzzerCh, buzzerPWM.Top()/2)
}

// startWatchdog arms the hardware watchdog. A failure to configure it is not
// fatal — the badge simply loses its self-recovery safety net.
func startWatchdog() {
	if err := machine.Watchdog.Configure(machine.WatchdogConfig{TimeoutMillis: watchdogTimeoutMillis}); err != nil {
		println("watchdog configure:", err.Error())

		return
	}

	if err := machine.Watchdog.Start(); err != nil {
		println("watchdog start:", err.Error())
	}
}

func feedWatchdog() {
	machine.Watchdog.Update()
}
