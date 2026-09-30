package main

import "image/color"

var (
	ledOff   = color.RGBA{0, 0, 0, 255}
	ledRed   = color.RGBA{50, 0, 0, 255}
	ledCyan  = color.RGBA{0, 14, 20, 255}
	ledGreen = color.RGBA{0, 18, 0, 255}
	ledAmber = color.RGBA{45, 18, 0, 255}
	ledBlue  = color.RGBA{0, 0, 12, 255}
)

// ledsOff darkens both NeoPixels (used while the badge rests or in standby).
func ledsOff() {
	setEyes(ledOff, ledOff)
}

// updateLEDs reflects the current state on the gopher's eyes:
//
//	no link             steady blue
//	needs permission    eyes alternate red, left/right (never stops)
//	new wait (~8s)      both eyes blink amber together
//	Claude is working   steady dim cyan
//	someone waits       steady amber
//	all quiet           steady green
//
// Only states that want something from you are allowed to move: a blinking
// "I am busy" indicator is pure distraction, and the header spinner already
// shows work in flight. So work gets a colour, not motion.
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
		setEyes(ledCyan, ledCyan)
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
