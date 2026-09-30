// Package gesture turns a stream of touch samples into taps, long presses
// and swipes. It is deliberately small: a resistive panel under a finger
// jitters by a few pixels, so "did not move" means "moved less than the
// slop", and a long press fires while the finger is still down so the user
// gets feedback without lifting.
package gesture

import "time"

// Kind is the recognised gesture.
type Kind uint8

const (
	None Kind = iota
	Tap
	LongPress
	SwipeLeft
	SwipeRight
	SwipeUp
	SwipeDown
)

// Sample is one touch reading: the position while Down, ignored otherwise.
type Sample struct {
	X, Y int
	Down bool
	At   time.Time
}

// Event is a recognised gesture; X and Y are where the finger landed.
type Event struct {
	Kind Kind
	X, Y int
}

// Config tunes the recognizer.
type Config struct {
	LongPress time.Duration // hold this long without moving to long-press
	SwipeMin  int           // travel at least this far to swipe
	TapSlop   int           // move less than this to still count as a tap
}

// Recognizer holds one press in flight. The zero value is unusable: use New.
type Recognizer struct {
	cfg       Config
	down      bool
	ignored   bool
	longFired bool
	x0, y0    int
	x, y      int
	t0        time.Time
}

func New(cfg Config) Recognizer {
	return Recognizer{cfg: cfg}
}

// Pressed reports whether a finger is currently down.
func (r *Recognizer) Pressed() bool {
	return r.down
}

// Ignore discards the press in flight: nothing is reported until the finger
// lifts. The caller uses it when a touch already did its job, e.g. woke the
// screen from standby.
func (r *Recognizer) Ignore() {
	r.ignored = true
}

// Feed consumes one sample and returns the gesture it completed, if any.
func (r *Recognizer) Feed(s Sample) Event {
	if s.Down && !r.down {
		r.down = true
		r.ignored = false
		r.longFired = false
		r.x0, r.y0, r.x, r.y = s.X, s.Y, s.X, s.Y
		r.t0 = s.At

		return Event{}
	}

	if !s.Down {
		if !r.down {
			return Event{}
		}
		r.down = false

		return r.release()
	}

	r.x, r.y = s.X, s.Y
	if r.ignored || r.longFired {
		return Event{}
	}

	if r.within(r.cfg.TapSlop) && s.At.Sub(r.t0) >= r.cfg.LongPress {
		r.longFired = true

		return Event{Kind: LongPress, X: r.x0, Y: r.y0}
	}

	return Event{}
}

// release classifies the finished press.
func (r *Recognizer) release() Event {
	if r.ignored || r.longFired {
		return Event{}
	}

	dx, dy := r.x-r.x0, r.y-r.y0
	adx, ady := abs(dx), abs(dy)

	switch {
	case adx >= r.cfg.SwipeMin && adx >= ady:
		if dx < 0 {
			return Event{Kind: SwipeLeft, X: r.x0, Y: r.y0}
		}

		return Event{Kind: SwipeRight, X: r.x0, Y: r.y0}
	case ady >= r.cfg.SwipeMin:
		if dy < 0 {
			return Event{Kind: SwipeUp, X: r.x0, Y: r.y0}
		}

		return Event{Kind: SwipeDown, X: r.x0, Y: r.y0}
	case r.within(r.cfg.TapSlop):
		return Event{Kind: Tap, X: r.x0, Y: r.y0}
	}

	return Event{}
}

// within reports whether the finger stayed inside a box of the given half
// width around where it landed.
func (r *Recognizer) within(slop int) bool {
	return abs(r.x-r.x0) < slop && abs(r.y-r.y0) < slop
}

func abs(v int) int {
	if v < 0 {
		return -v
	}

	return v
}
