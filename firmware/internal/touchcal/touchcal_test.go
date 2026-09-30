package touchcal

import "testing"

// A panel whose raw X grows with screen X and raw Y grows with screen Y.
var straight = struct{ tl, tr, bl Raw }{
	tl: Raw{X: 300, Y: 400},
	tr: Raw{X: 3700, Y: 400},
	bl: Raw{X: 300, Y: 3600},
}

// The spike's panel: raw X runs along the screen's Y axis and backwards.
var swapped = struct{ tl, tr, bl Raw }{
	tl: Raw{X: 3600, Y: 300},
	tr: Raw{X: 3600, Y: 3700},
	bl: Raw{X: 400, Y: 300},
}

func TestFromTapsStraight(t *testing.T) {
	c := FromTaps(straight.tl, straight.tr, straight.bl)
	if !c.Valid || c.Swap {
		t.Fatalf("got %+v, want valid, not swapped", c)
	}
	if x, y := c.Map(straight.tl, 320, 240); x != RefLeft || y != RefTop {
		t.Fatalf("top-left maps to %d,%d", x, y)
	}
	if x, y := c.Map(straight.tr, 320, 240); x != RefRight || y != RefTop {
		t.Fatalf("top-right maps to %d,%d", x, y)
	}
	if x, y := c.Map(straight.bl, 320, 240); x != RefLeft || y != RefBottom {
		t.Fatalf("bottom-left maps to %d,%d", x, y)
	}
	// Midpoint between the two top crosses.
	if x, _ := c.Map(Raw{X: 2000, Y: 400}, 320, 240); x != 160 {
		t.Fatalf("midpoint x = %d", x)
	}
}

func TestFromTapsSwappedAndInverted(t *testing.T) {
	c := FromTaps(swapped.tl, swapped.tr, swapped.bl)
	if !c.Valid || !c.Swap {
		t.Fatalf("got %+v, want valid and swapped", c)
	}
	if x, y := c.Map(swapped.tr, 320, 240); x != RefRight || y != RefTop {
		t.Fatalf("top-right maps to %d,%d", x, y)
	}
	if x, y := c.Map(swapped.bl, 320, 240); x != RefLeft || y != RefBottom {
		t.Fatalf("bottom-left maps to %d,%d", x, y)
	}
}

func TestMapClampsToTheScreen(t *testing.T) {
	c := FromTaps(straight.tl, straight.tr, straight.bl)
	if x, y := c.Map(Raw{X: 0, Y: 0}, 320, 240); x != 0 || y != 0 {
		t.Fatalf("below range maps to %d,%d", x, y)
	}
	if x, y := c.Map(Raw{X: 4095, Y: 4095}, 320, 240); x != 319 || y != 239 {
		t.Fatalf("above range maps to %d,%d", x, y)
	}
}

func TestFromTapsRejectsDegenerate(t *testing.T) {
	same := Raw{X: 1000, Y: 1000}
	if c := FromTaps(same, same, same); c.Valid {
		t.Fatalf("three identical taps accepted: %+v", c)
	}
	// Two crosses hit, the third on top of the first: no Y span.
	if c := FromTaps(straight.tl, straight.tr, straight.tl); c.Valid {
		t.Fatalf("missing Y span accepted: %+v", c)
	}
}

func TestInvalidCalMapsToCenter(t *testing.T) {
	var c Cal
	if x, y := c.Map(Raw{X: 5, Y: 5}, 320, 240); x != 160 || y != 120 {
		t.Fatalf("invalid cal maps to %d,%d", x, y)
	}
}
