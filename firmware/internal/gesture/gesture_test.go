package gesture

import (
	"testing"
	"time"
)

var testCfg = Config{LongPress: 1500 * time.Millisecond, QuickTap: 300 * time.Millisecond, SwipeMin: 50, TapSlop: 20}

func press(r *Recognizer, t0 time.Time, points [][2]int, step time.Duration) Event {
	var last Event
	at := t0
	for _, p := range points {
		last = r.Feed(Sample{X: p[0], Y: p[1], Down: true, At: at})
		at = at.Add(step)
	}
	return last
}

func TestTapOnRelease(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	if ev := press(&r, t0, [][2]int{{100, 100}, {102, 101}, {103, 101}}, 20*time.Millisecond); ev.Kind != None {
		t.Fatalf("event while still pressed: %+v", ev)
	}
	ev := r.Feed(Sample{Down: false, At: t0.Add(80 * time.Millisecond)})
	if ev.Kind != Tap || ev.X != 102 || ev.Y != 101 {
		t.Fatalf("got %+v, want Tap at the median 102,101", ev)
	}
}

func TestTapPositionIsTheMedian(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	// One wild sample in the middle of a steady press must not move the tap.
	press(&r, t0, [][2]int{{150, 120}, {151, 121}, {168, 135}, {150, 119}, {149, 120}}, 20*time.Millisecond)
	ev := r.Feed(Sample{Down: false, At: t0.Add(120 * time.Millisecond)})
	if ev.Kind != Tap || ev.X != 150 || ev.Y != 120 {
		t.Fatalf("got %+v, want Tap at 150,120", ev)
	}
}

func TestLongPressFiresWhileHeld(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	r.Feed(Sample{X: 50, Y: 220, Down: true, At: t0})
	if ev := r.Feed(Sample{X: 51, Y: 221, Down: true, At: t0.Add(1400 * time.Millisecond)}); ev.Kind != None {
		t.Fatalf("too early: %+v", ev)
	}
	ev := r.Feed(Sample{X: 51, Y: 221, Down: true, At: t0.Add(1600 * time.Millisecond)})
	if ev.Kind != LongPress || ev.X != 50 || ev.Y != 220 {
		t.Fatalf("got %+v, want LongPress at 50,220", ev)
	}
	// Holding on and releasing produces nothing more.
	if ev := r.Feed(Sample{X: 51, Y: 221, Down: true, At: t0.Add(2 * time.Second)}); ev.Kind != None {
		t.Fatalf("repeated long press: %+v", ev)
	}
	if ev := r.Feed(Sample{Down: false, At: t0.Add(2100 * time.Millisecond)}); ev.Kind != None {
		t.Fatalf("release after long press: %+v", ev)
	}
}

func TestSwipeDirections(t *testing.T) {
	cases := []struct {
		name   string
		dx, dy int
		want   Kind
	}{
		{"left", -200, 5, SwipeLeft},
		{"right", 220, -8, SwipeRight},
		{"up", 3, -150, SwipeUp},
		{"down", -10, 160, SwipeDown},
		{"diagonal picks the dominant axis", 200, 120, SwipeRight},
	}
	for _, c := range cases {
		r := New(testCfg)
		t0 := time.Unix(0, 0)
		// A real swipe: six samples from (160,120) to (160+dx, 120+dy).
		for i := 0; i <= 5; i++ {
			r.Feed(Sample{X: 160 + c.dx*i/5, Y: 120 + c.dy*i/5, Down: true, At: t0.Add(time.Duration(i) * 20 * time.Millisecond)})
		}
		ev := r.Feed(Sample{Down: false, At: t0.Add(150 * time.Millisecond)})
		if ev.Kind != c.want {
			t.Errorf("%s: got %v, want %v", c.name, ev.Kind, c.want)
		}
	}
}

func TestLiftOutlierDoesNotMakeASwipe(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	// A steady tap whose last reading, taken as the finger lifts, is garbage.
	press(&r, t0, [][2]int{{150, 120}, {151, 121}, {150, 120}, {152, 119}, {151, 120}, {4, 236}}, 20*time.Millisecond)
	ev := r.Feed(Sample{Down: false, At: t0.Add(130 * time.Millisecond)})
	if ev.Kind != Tap || abs(ev.X-150) > 2 || abs(ev.Y-120) > 2 {
		t.Fatalf("got %+v, want a Tap near 150,120", ev)
	}
}

func TestLandingOutlierDoesNotMakeASwipe(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	press(&r, t0, [][2]int{{300, 10}, {150, 120}, {151, 121}, {150, 120}, {152, 119}}, 20*time.Millisecond)
	ev := r.Feed(Sample{Down: false, At: t0.Add(110 * time.Millisecond)})
	if ev.Kind != Tap {
		t.Fatalf("got %+v, want a Tap", ev)
	}
}

func TestTooFewSamplesCannotSwipe(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	press(&r, t0, [][2]int{{100, 100}, {200, 100}, {300, 100}}, 20*time.Millisecond)
	if ev := r.Feed(Sample{Down: false, At: t0.Add(60 * time.Millisecond)}); ev.Kind == SwipeRight {
		t.Fatalf("three samples made a swipe: %+v", ev)
	}
}

func TestQuickSloppyPressIsATap(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	r.Feed(Sample{X: 100, Y: 100, Down: true, At: t0})
	r.Feed(Sample{X: 128, Y: 104, Down: true, At: t0.Add(30 * time.Millisecond)}) // beyond TapSlop, short of SwipeMin
	if ev := r.Feed(Sample{Down: false, At: t0.Add(60 * time.Millisecond)}); ev.Kind != Tap {
		t.Fatalf("got %+v, want Tap", ev)
	}
}

func TestSlowDriftIsNotATap(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	r.Feed(Sample{X: 100, Y: 100, Down: true, At: t0})
	r.Feed(Sample{X: 128, Y: 104, Down: true, At: t0.Add(400 * time.Millisecond)})
	if ev := r.Feed(Sample{Down: false, At: t0.Add(600 * time.Millisecond)}); ev.Kind != None {
		t.Fatalf("got %+v, want None", ev)
	}
}

func TestLongPressCancelledByMovement(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	r.Feed(Sample{X: 100, Y: 100, Down: true, At: t0})
	r.Feed(Sample{X: 130, Y: 100, Down: true, At: t0.Add(200 * time.Millisecond)})
	if ev := r.Feed(Sample{X: 130, Y: 100, Down: true, At: t0.Add(2 * time.Second)}); ev.Kind != None {
		t.Fatalf("long press after moving: %+v", ev)
	}
}

func TestRecognizerIgnoreSwallowsPress(t *testing.T) {
	r := New(testCfg)
	t0 := time.Unix(0, 0)
	r.Feed(Sample{X: 100, Y: 100, Down: true, At: t0})
	r.Ignore() // the caller used this touch to wake the screen
	if ev := r.Feed(Sample{X: 100, Y: 100, Down: true, At: t0.Add(2 * time.Second)}); ev.Kind != None {
		t.Fatalf("long press on an ignored press: %+v", ev)
	}
	if ev := r.Feed(Sample{Down: false, At: t0.Add(2100 * time.Millisecond)}); ev.Kind != None {
		t.Fatalf("release of an ignored press: %+v", ev)
	}
	// The next press is a normal one again.
	r.Feed(Sample{X: 10, Y: 10, Down: true, At: t0.Add(3 * time.Second)})
	if ev := r.Feed(Sample{Down: false, At: t0.Add(3050 * time.Millisecond)}); ev.Kind != Tap {
		t.Fatalf("press after ignore: %+v", ev)
	}
}

func TestPressed(t *testing.T) {
	r := New(testCfg)
	if r.Pressed() {
		t.Fatal("pressed before any sample")
	}
	r.Feed(Sample{X: 1, Y: 1, Down: true, At: time.Unix(0, 0)})
	if !r.Pressed() {
		t.Fatal("not pressed while down")
	}
	r.Feed(Sample{Down: false, At: time.Unix(1, 0)})
	if r.Pressed() {
		t.Fatal("pressed after release")
	}
}
