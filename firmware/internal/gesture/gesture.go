// Package gesture turns a stream of touch samples into taps, long presses
// and swipes. It is deliberately small, and forgiving where a resistive
// panel needs it: readings jitter by up to ~20 px under a light finger, so
// a tap is reported at the median of its samples, a quick press that
// wandered a little is still a tap, and only real travel becomes a swipe. A
// long press fires while the finger is still down so the user gets feedback
// without lifting.
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

// Event is a recognised gesture; X and Y are where it happened (the median
// position of a tap, the landing point of a swipe or long press).
type Event struct {
	Kind Kind
	X, Y int
}

// Config tunes the recognizer.
type Config struct {
	LongPress time.Duration // hold this long without moving to long-press
	QuickTap  time.Duration // a press shorter than this is a tap even past TapSlop
	SwipeMin  int           // travel at least this far to swipe
	TapSlop   int           // move less than this to still count as a tap
}

const (
	// medianWindow is how many samples of a press feed the tap position; a
	// tap is a handful of 20 ms samples, a longer press keeps the last ones.
	medianWindow = 9

	// A swipe's start and end are medians of the first and last endpointWindow
	// samples: the landing and lifting readings are the noisiest and a single
	// outlier there must not turn a tap into a swipe. Fewer than minSwipeSamples
	// samples cannot establish a direction at all.
	endpointWindow  = 3
	minSwipeSamples = 4
)

// Recognizer holds one press in flight. The zero value is unusable: use New.
type Recognizer struct {
	cfg       Config
	down      bool
	ignored   bool
	longFired bool
	x0, y0    int
	x, y      int
	t0        time.Time

	xs, ys [medianWindow]int
	n      int // samples recorded, capped at medianWindow (ring)
	next   int // ring write index
	total  int // samples recorded since the press began

	hx, hy [endpointWindow]int // the first samples of the press
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
		r.n, r.next, r.total = 0, 0, 0
		r.record(s.X, s.Y)

		return Event{}
	}

	if !s.Down {
		if !r.down {
			return Event{}
		}
		r.down = false

		return r.release(s.At)
	}

	r.x, r.y = s.X, s.Y
	r.record(s.X, s.Y)
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
func (r *Recognizer) release(at time.Time) Event {
	if r.ignored || r.longFired {
		return Event{}
	}

	dx, dy := r.travel()
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
	case r.within(r.cfg.TapSlop) || at.Sub(r.t0) < r.cfg.QuickTap:
		x, y := r.median()

		return Event{Kind: Tap, X: x, Y: y}
	}

	return Event{}
}

// within reports whether the finger stayed inside a box of the given half
// width around where it landed.
func (r *Recognizer) within(slop int) bool {
	return abs(r.x-r.x0) < slop && abs(r.y-r.y0) < slop
}

func (r *Recognizer) record(x, y int) {
	if r.total < endpointWindow {
		r.hx[r.total], r.hy[r.total] = x, y
	}
	r.total++

	r.xs[r.next], r.ys[r.next] = x, y
	r.next = (r.next + 1) % medianWindow
	if r.n < medianWindow {
		r.n++
	}
}

// travel is the displacement between the median of the first samples and
// the median of the last ones; zero while the press is too short to tell.
func (r *Recognizer) travel() (dx, dy int) {
	if r.total < minSwipeSamples {
		return 0, 0
	}

	var hx, hy [endpointWindow]int
	hx, hy = r.hx, r.hy
	sortInts(hx[:])
	sortInts(hy[:])

	var tx, ty [endpointWindow]int
	for i := 0; i < endpointWindow; i++ {
		idx := (r.next - 1 - i + medianWindow) % medianWindow
		tx[i], ty[i] = r.xs[idx], r.ys[idx]
	}
	sortInts(tx[:])
	sortInts(ty[:])

	mid := endpointWindow / 2

	return tx[mid] - hx[mid], ty[mid] - hy[mid]
}

// median returns the per-axis median of the recorded samples; with few
// samples it degrades gracefully to the middle one.
func (r *Recognizer) median() (int, int) {
	var xs, ys [medianWindow]int
	copy(xs[:], r.xs[:r.n])
	copy(ys[:], r.ys[:r.n])
	sortInts(xs[:r.n])
	sortInts(ys[:r.n])

	return xs[r.n/2], ys[r.n/2]
}

// sortInts is an insertion sort: the window is tiny and the firmware has no
// use for sort.Ints' allocations.
func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}

	return v
}
