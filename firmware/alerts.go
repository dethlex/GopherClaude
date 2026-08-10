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
	ledRed   = color.RGBA{50, 0, 0, 255}
	ledCyan  = color.RGBA{0, 14, 20, 255}
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

// setEyes writes both NeoPixels at once.
func setEyes(left, right color.RGBA) {
	ledColors[0] = left
	ledColors[1] = right
	leds.WriteColors(ledColors[:])
}

// ledsOff darkens both NeoPixels (used while the badge rests or in standby).
func ledsOff() {
	setEyes(ledOff, ledOff)
}

// updateLEDs reflects the current state on the gopher's eyes:
//
//	no link             steady blue
//	needs permission    eyes alternate red, left/right (never stops)
//	new wait (~8s)      both eyes blink amber together
//	Claude is working   eyes alternate dim cyan, left/right
//	someone waits       steady amber
//	all quiet           steady green
//
// Alternating reads differently from a synchronised blink even in peripheral
// vision, so "something is happening" never looks like "you are needed".
//
// The order matters more than the patterns: a fresh alert wins for its short
// window, then work-in-progress outranks a stale wait — otherwise one session
// parked in "waiting" for hours would mask every other state forever.
func updateLEDs(f frame, linked, alerting, blinkOn bool) {
	if !linked {
		setEyes(ledBlue, ledBlue)

		return
	}

	perm, input := waitingKinds(f)

	switch {
	case perm > 0:
		// A permission dialog blocks the session outright, so this one
		// keeps alternating until it is answered or muted.
		alternate(ledRed, blinkOn)
	case alerting && input > 0:
		if blinkOn {
			setEyes(ledAmber, ledAmber)
		} else {
			ledsOff()
		}
	case workingSessions(f) > 0:
		alternate(ledCyan, blinkOn)
	case input > 0:
		setEyes(ledAmber, ledAmber)
	default:
		setEyes(ledGreen, ledGreen)
	}
}

// alternate lights one eye at a time, swapping sides every tick.
func alternate(c color.RGBA, leftFirst bool) {
	if leftFirst {
		setEyes(c, ledOff)

		return
	}

	setEyes(ledOff, c)
}
