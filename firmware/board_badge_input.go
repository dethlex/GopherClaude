//go:build gopher_badge

package main

import (
	"machine"
	"time"
)

const (
	// The accelerometer is sampled at the blink rate, as before the input
	// seam existed; two consecutive lying-flat reads mute (restDebounce).
	accelPeriod = blinkPeriod
	restDebounce = 2

	// Total accelerometer delta (sum over 3 axes, micro-g) that counts as
	// "the badge was picked up / nudged" and wakes it from standby.
	motionWakeMicroG = 150_000
)

var (
	btnA     = button{pin: machine.BUTTON_A}
	btnB     = button{pin: machine.BUTTON_B}
	btnUp    = button{pin: machine.BUTTON_UP}
	btnDown  = button{pin: machine.BUTTON_DOWN}
	btnLeft  = button{pin: machine.BUTTON_LEFT}
	btnRight = button{pin: machine.BUTTON_RIGHT}

	lastAccel time.Time
	restTicks int
	resting   bool
)

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

func initInput() {
	for _, p := range []machine.Pin{machine.BUTTON_A, machine.BUTTON_B, machine.BUTTON_UP,
		machine.BUTTON_DOWN, machine.BUTTON_LEFT, machine.BUTTON_RIGHT} {
		p.Configure(machine.PinConfig{Mode: machine.PinInput})
	}

	initAccel()
}

// pollInput reports one event per call. Buttons are checked in a switch so
// an edge that is not consumed this round survives to the next 10 ms poll;
// the accelerometer runs on its own slower clock.
func pollInput(now time.Time, standby bool) input {
	switch {
	case btnA.pressed():
		return input{kind: evFocus}
	case btnUp.pressed():
		return input{kind: evViewPrev}
	case btnDown.pressed():
		return input{kind: evViewNext}
	case btnB.pressed():
		return input{kind: evSoundToggle}
	case btnLeft.pressed() || btnRight.pressed():
		return input{kind: evPageToggle}
	}

	if now.Sub(lastAccel) < accelPeriod {
		return input{}
	}
	lastAccel = now

	z, delta, ok := readAccel()
	if !ok {
		return input{}
	}

	if z < restThresholdMicroG {
		if restTicks < restDebounce {
			restTicks++
		}
	} else {
		restTicks = 0
	}

	nowResting := restTicks >= restDebounce

	switch {
	case nowResting && !resting:
		resting = true

		return input{kind: evRest}
	case !nowResting && resting:
		resting = false

		return input{kind: evUnrest}
	case delta > motionWakeMicroG:
		return input{kind: evWake}
	}

	return input{}
}
