// Package touchcal maps raw resistive-panel readings to screen pixels from
// three calibration taps. Three crosses, not two: with top-left → top-right
// only the screen's X changes, so whichever raw axis moved most is the X
// axis; top-left → bottom-left settles Y the same way. That also finds a
// panel wired sideways or backwards (the spike's unit had Y inverted).
package touchcal

// Screen coordinates of the three crosses.
const (
	RefLeft   = 20
	RefRight  = 300
	RefTop    = 20
	RefBottom = 220

	// A cross tap must move the raw reading at least this much along the
	// axis being calibrated; anything less is a repeated tap on one spot.
	minSpan = 200
)

// Raw is one 12-bit reading of the panel.
type Raw struct {
	X, Y int
}

// Cal maps raw readings to pixels. X0/X1 are the raw values seen at RefLeft
// and RefRight on the raw axis that runs along the screen's X (raw Y when
// Swap is set); Y0/Y1 likewise for RefTop and RefBottom. int16 keeps the
// record small for the settings sector.
type Cal struct {
	Swap           bool
	X0, X1, Y0, Y1 int16
	Valid          bool
}

// FromTaps derives a calibration from the three cross taps. A degenerate set
// (no usable span on an axis) yields an invalid Cal.
func FromTaps(topLeft, topRight, bottomLeft Raw) Cal {
	dxX, dyX := abs(topRight.X-topLeft.X), abs(topRight.Y-topLeft.Y) // moving along screen X
	swap := dyX > dxX

	a := func(r Raw) int { // raw axis along screen X
		if swap {
			return r.Y
		}

		return r.X
	}
	b := func(r Raw) int { // raw axis along screen Y
		if swap {
			return r.X
		}

		return r.Y
	}

	c := Cal{
		Swap: swap,
		X0:   int16(a(topLeft)),
		X1:   int16(a(topRight)),
		Y0:   int16(b(topLeft)),
		Y1:   int16(b(bottomLeft)),
	}
	c.Valid = abs(int(c.X1)-int(c.X0)) >= minSpan && abs(int(c.Y1)-int(c.Y0)) >= minSpan

	return c
}

// Map converts a raw reading to a pixel on a w×h screen, clamped to the
// screen. An invalid calibration maps everything to the centre, so a wrong
// tap can do no worse than hit the middle of the dashboard.
func (c Cal) Map(r Raw, w, h int) (x, y int) {
	if !c.Valid {
		return w / 2, h / 2
	}

	ax, ay := r.X, r.Y
	if c.Swap {
		ax, ay = ay, ax
	}

	x = RefLeft + (ax-int(c.X0))*(RefRight-RefLeft)/(int(c.X1)-int(c.X0))
	y = RefTop + (ay-int(c.Y0))*(RefBottom-RefTop)/(int(c.Y1)-int(c.Y0))

	return clamp(x, w), clamp(y, h)
}

func clamp(v, limit int) int {
	if v < 0 {
		return 0
	}
	if v >= limit {
		return limit - 1
	}

	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}

	return v
}
