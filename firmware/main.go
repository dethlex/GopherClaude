// ClaudeControl firmware for the Gopher Badge (tinygo, target gopher-badge).
//
// The badge renders the state of the coding assistants running on the host
// (Claude Code, Antigravity, Codex): plan limits, the number of active chats
// and an alert banner when a session waits for the user. Data arrives over USB
// CDC serial from the companion host agent (cmd/agent in this repository); the
// badge itself has no network.
package main

import (
	"machine"
	"strconv"
	"time"
)

const (
	pollInterval = 10 * time.Millisecond
	linkTimeout  = 10 * time.Second
	blinkPeriod  = 400 * time.Millisecond
	usbEnumDelay = 1 * time.Second

	// How long the eyes blink after a new alert; after that they stay a
	// steady dim amber while sessions wait. Constant blinking is annoying
	// because "waiting for input" is the badge's normal resting state.
	alertBlinkWindow = 8 * time.Second

	// Two consecutive lying-flat accelerometer reads (one blink period
	// apart) are required before muting, so a brief wobble does nothing.
	restDebounce = 2

	// Standby: with no activity for this long the screen and eyes turn
	// off. Activity = data actually changing (tokens growing, chats/wait
	// counts, a new alert), a button press, or the badge being moved.
	standbyTimeout = 5 * time.Minute

	// Total accelerometer delta (sum over 3 axes, micro-g) that counts as
	// "the badge was picked up / nudged" and wakes it from standby.
	motionWakeMicroG = 150_000

	// How often to re-beep while a session keeps waiting, so a blocked
	// chat is not forgotten. Button A / sound-off / rest all silence it.
	renudgeInterval = 2 * time.Minute

	// Spinner animation step: 8 steps per revolution, ~1s per turn. Purely
	// visual — it never counts as activity, so it cannot hold off standby.
	spinnerPeriod = 125 * time.Millisecond

	// Hardware watchdog: if the main loop ever stalls this long (e.g. a
	// wedged I2C read of the accelerometer), the chip resets itself and
	// the firmware restarts instead of freezing on a stale screen. The
	// loop feeds it every iteration; nothing in the loop blocks anywhere
	// near this long (the longest beep is well under a second).
	watchdogTimeoutMillis = 4000
)

func main() {
	time.Sleep(usbEnumDelay)

	initDisplay()
	initButtons()
	initBuzzer()
	initLEDs()
	initAccel()

	// Restore saved preferences before the first render so the sound icon
	// reflects them immediately.
	soundOff = loadSettings().soundOff

	var (
		state        frame
		linked       bool
		muted        bool
		blinkOn      bool
		standby      bool
		lastRx       time.Time
		lastBlink    time.Time
		lastSpin     time.Time
		alertUntil   time.Time
		lastNudge    time.Time
		lastActivity = time.Now()
		restTicks    int
	)

	// touch registers activity: it postpones standby and, when already in
	// standby, wakes the badge up (the panel content is intact under the
	// disabled backlight, so no redraw is needed).
	touch := func(t time.Time) {
		lastActivity = t

		if standby {
			standby = false
			setBacklight(true)
			updateLEDs(state, linked, alerting(t, alertUntil, muted), blinkOn)
		}
	}

	drawStaticUI(state)
	render(state, linked)
	updateLEDs(state, linked, false, blinkOn)

	startWatchdog()

	for {
		machine.Watchdog.Update()

		now := time.Now()
		isResting := restTicks >= restDebounce

		if line, ok := pollSerialLine(); ok {
			f, err := parseFrame(line)
			if err != nil {
				println("err: bad frame")
			} else {
				newAlert := f.totalWait() > 0 && (!linked || state.totalWait() == 0 || f.msg != state.msg)
				if f.totalWait() == 0 || f.msg != state.msg {
					// A new event unmutes — unless the badge
					// deliberately rests on the desk.
					muted = isResting
				}

				// Session minutes tick every minute and must not
				// keep the badge awake; only real work counts.
				dataChanged := !linked || f.totalChats() != state.totalChats() || f.totalWait() != state.totalWait() ||
					f.tokIn != state.tokIn || f.tokOut != state.tokOut || f.msg != state.msg ||
					promptsChanged(f, state)

				state = f
				linked = true
				lastRx = now

				if dataChanged {
					touch(now)
				}

				// A changed assistant set rewrites the labels and
				// marks, so the chrome goes with it.
				if syncProviders(state) {
					repaintAll(state, linked)
				} else {
					render(state, linked)
				}

				if newAlert && !muted {
					alertUntil = now.Add(alertBlinkWindow)
					lastNudge = now
					beepAlert()
				}

				if !isResting && !standby {
					updateLEDs(state, linked, alerting(now, alertUntil, muted), blinkOn)
				}

				println("ok chats=" + strconv.Itoa(f.totalChats()) + " wait=" + strconv.Itoa(f.totalWait()))
			}
		}

		if linked && now.Sub(lastRx) > linkTimeout {
			linked = false
			touch(now) // surface NO LINK before dozing off again

			render(state, linked)

			if !isResting && !standby {
				updateLEDs(state, linked, false, blinkOn)
			}
		}

		if btnA.pressed() {
			touch(now)

			// Open a chat on the Mac. On the session list, open the
			// highlighted row by its index in the frame (the list is
			// filtered and paged locally); on the dashboard, the
			// alerting one. Muting lives on button B / lay-flat.
			switch {
			case activePage == pageSessions && listCount > 0:
				sendCommand("focus " + strconv.Itoa(listRows[selRow]))
			case activePage == pageDashboard && state.totalWait() > 0:
				sendCommand("focus")
			}
		}

		// Up/down: move the cursor on the session list, cycle the dashboard
		// views (CLAUDE, one per extra assistant, ALL) otherwise.
		if btnUp.pressed() {
			touch(now)

			if activePage == pageSessions {
				moveSelection(state, -1)
				render(state, linked)
			} else {
				switchView(state, linked, -1)
			}
		}

		if btnDown.pressed() {
			touch(now)

			if activePage == pageSessions {
				moveSelection(state, 1)
				render(state, linked)
			} else {
				switchView(state, linked, 1)
			}
		}

		if btnB.pressed() {
			touch(now)

			soundOff = !soundOff

			if soundOff {
				tone(0)
			} else {
				beepSoundOn()
			}

			render(state, linked)
			saveSettings(settings{soundOff: soundOff})
		}

		if btnLeft.pressed() || btnRight.pressed() {
			touch(now)
			switchPage(state, linked)
		}

		// Re-nudge: remind about a still-waiting session. Audio only —
		// it nags even from standby (the whole point), but rest/mute/
		// sound-off silence it.
		if linked && state.totalWait() > 0 && !muted && !soundOff && !isResting &&
			now.Sub(lastNudge) >= renudgeInterval {
			beepNudge()
			lastNudge = now
		}

		if now.Sub(lastBlink) >= blinkPeriod {
			blinkOn = !blinkOn
			lastBlink = now

			z, delta, accelRead := readAccel()

			if accelRead && z < restThresholdMicroG {
				if restTicks < restDebounce {
					restTicks++
				}
			} else {
				restTicks = 0
			}

			if accelRead && delta > motionWakeMicroG {
				touch(now)
			}

			nowResting := restTicks >= restDebounce

			switch {
			case nowResting && !isResting:
				// Just laid down on the desk: silence everything
				// and confirm with a descending chirp. Rest
				// overrides standby; pickup re-arms it.
				muted = true
				standby = false
				tone(0)
				setBacklight(false)
				ledsOff()
				beepFlipDown()
			case !nowResting && isResting:
				// Picked back up: lights on (the panel content is
				// intact), ascending chirp. Current alerts stay
				// muted until a new event arrives.
				touch(now)
				setBacklight(true)
				beepFlipUp()
				updateLEDs(state, linked, alerting(now, alertUntil, muted), blinkOn)
			case !nowResting && !standby:
				updateLEDs(state, linked, alerting(now, alertUntil, muted), blinkOn)
			}
			// While resting or in standby: screen and eyes stay dark.
		}

		// The spinner runs on its own timer and also picks up work
		// starting/stopping, so no extra redraw is needed elsewhere.
		if now.Sub(lastSpin) >= spinnerPeriod {
			lastSpin = now

			advanceSpinner()
			renderSpinner(state, linked, standby || restTicks >= restDebounce)
		}

		if !isResting && !standby && now.Sub(lastActivity) > standbyTimeout {
			standby = true
			setBacklight(false)
			ledsOff()
		}

		time.Sleep(pollInterval)
	}
}

// alerting reports whether the short attention-grabbing blink window is open.
func alerting(now, alertUntil time.Time, muted bool) bool {
	return !muted && now.Before(alertUntil)
}

// promptsChanged reports whether a secondary assistant's daily prompt count
// moved (or the set of assistants itself did): a new prompt is real activity
// worth waking the badge for, unlike session minutes ticking.
func promptsChanged(a, b frame) bool {
	if a.extraCount != b.extraCount {
		return true
	}

	for i := 0; i < a.extraCount; i++ {
		if a.extras[i].prov != b.extras[i].prov || a.extras[i].prompts != b.extras[i].prompts {
			return true
		}
	}

	return false
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
