package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers/ws2812"
)

const (
	beepFreqHigh = 880
	beepFreqLow  = 660
	beepDuration = 120 * time.Millisecond
	beepPause    = 80 * time.Millisecond

	// Flip feedback chirps are shorter and span a wider interval than the
	// alert beep, so the two are easy to tell apart by ear.
	flipFreqHigh     = 1320
	flipFreqLow      = 440
	flipBeepDuration = 70 * time.Millisecond

	// Re-nudge: a single mid tone, distinct from the alert's two-tone
	// chirp, repeated while a session keeps waiting.
	nudgeDuration = 100 * time.Millisecond
)

var (
	// soundOff is the global beep kill-switch, toggled with button B. It
	// silences every chirp (alerts and rest/wake feedback); LEDs and the
	// screen keep working.
	soundOff bool

	leds      ws2812.Device
	ledColors [2]color.RGBA

	buzzerPWM = machine.PWM7 // machine.SPEAKER (GPIO14) is on PWM slice 7
	buzzerCh  uint8
	buzzerOK  bool

	btnA     = button{pin: machine.BUTTON_A}
	btnB     = button{pin: machine.BUTTON_B}
	btnUp    = button{pin: machine.BUTTON_UP}
	btnDown  = button{pin: machine.BUTTON_DOWN}
	btnLeft  = button{pin: machine.BUTTON_LEFT}
	btnRight = button{pin: machine.BUTTON_RIGHT}

	ledOff   = color.RGBA{0, 0, 0, 255}
	ledGreen = color.RGBA{0, 18, 0, 255}
	ledAmber = color.RGBA{45, 18, 0, 255}
	ledBlue  = color.RGBA{0, 0, 12, 255}
)

func initLEDs() {
	neo := machine.NEOPIXELS
	neo.Configure(machine.PinConfig{Mode: machine.PinOutput})
	leds = ws2812.NewWS2812(neo)
}

// button is an active-low pin with single-press edge detection.
type button struct {
	pin  machine.Pin
	prev bool
}

func (b *button) pressed() bool {
	cur := !b.pin.Get()
	edge := cur && !b.prev
	b.prev = cur

	return edge
}

func initButtons() {
	machine.BUTTON_A.Configure(machine.PinConfig{Mode: machine.PinInput})
	machine.BUTTON_B.Configure(machine.PinConfig{Mode: machine.PinInput})
	machine.BUTTON_UP.Configure(machine.PinConfig{Mode: machine.PinInput})
	machine.BUTTON_DOWN.Configure(machine.PinConfig{Mode: machine.PinInput})
	machine.BUTTON_LEFT.Configure(machine.PinConfig{Mode: machine.PinInput})
	machine.BUTTON_RIGHT.Configure(machine.PinConfig{Mode: machine.PinInput})
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

// beepAlert plays a short two-tone chirp; blocks for ~0.4s which is fine for
// the 10ms main loop given the 512-byte serial RX ring and ~2s frame rate.
func beepAlert() {
	if soundOff {
		return
	}

	tone(beepFreqLow)
	time.Sleep(beepDuration)
	tone(beepFreqHigh)
	time.Sleep(beepDuration)
	tone(0)
}

// beepFlipDown is the descending "going to sleep" chirp.
func beepFlipDown() {
	if soundOff {
		return
	}

	tone(flipFreqHigh)
	time.Sleep(flipBeepDuration)
	tone(flipFreqLow)
	time.Sleep(flipBeepDuration)
	tone(0)
}

// beepFlipUp is the ascending "waking up" chirp.
func beepFlipUp() {
	if soundOff {
		return
	}

	tone(flipFreqLow)
	time.Sleep(flipBeepDuration)
	tone(flipFreqHigh)
	time.Sleep(flipBeepDuration)
	tone(0)
}

// beepSoundOn confirms that the buzzer was just re-enabled.
func beepSoundOn() {
	tone(beepFreqHigh)
	time.Sleep(flipBeepDuration)
	tone(0)
}

// beepNudge is the periodic "still waiting" reminder — one short low tone.
func beepNudge() {
	if soundOff {
		return
	}

	tone(beepFreqLow)
	time.Sleep(nudgeDuration)
	tone(0)
}

// ledsOff darkens both NeoPixels (used while the badge lies face-down).
func ledsOff() {
	ledColors[0] = ledOff
	ledColors[1] = ledOff
	leds.WriteColors(ledColors[:])
}

// updateLEDs reflects the current state on both NeoPixels (the gopher's
// eyes). They blink only while `alerting` — the short window right after a
// new event; otherwise waiting is shown as a steady dim amber.
func updateLEDs(f frame, linked, alerting, blinkOn bool) {
	c := ledGreen

	switch {
	case !linked:
		c = ledBlue
	case f.wait > 0 && alerting:
		if blinkOn {
			c = ledAmber
		} else {
			c = ledOff
		}
	case f.wait > 0:
		c = ledAmber
	}

	ledColors[0] = c
	ledColors[1] = c
	leds.WriteColors(ledColors[:])
}
