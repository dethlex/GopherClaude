package main

import "time"

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
)

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
