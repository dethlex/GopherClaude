package layout

import "testing"

func TestHitDashboardZones(t *testing.T) {
	cases := []struct {
		name string
		x, y int
		want Zone
	}{
		{"title strip left of the spinner", 60, 10, ZoneTitle},
		{"spinner is not the title", SpinnerX + 2, 10, ZoneNone},
		{"sound icon", SoundIconX + SoundIconW/2, SoundIconY + SoundIconH/2, ZoneSoundIcon},
		{"sound icon slop to the left", SoundIconX - 4, 6, ZoneSoundIcon},
		{"banner", 160, BannerTop + 10, ZoneBanner},
		{"bottom edge is banner", 319, ScreenH - 1, ZoneBanner},
		{"body is nothing on the dashboard", 160, 120, ZoneNone},
	}
	for _, c := range cases {
		if got := HitDashboard(c.x, c.y); got.Zone != c.want {
			t.Errorf("%s: HitDashboard(%d,%d) = %v, want %v", c.name, c.x, c.y, got.Zone, c.want)
		}
	}
}

func TestHitSessionsRows(t *testing.T) {
	for i := 0; i < SessRowsMax; i++ {
		top := SessRowBase - SessRowTopPad + i*SessRowStep
		for _, y := range []int{top, top + SessRowStep/2, top + SessRowStep - 1} {
			got := HitSessions(100, y)
			if got.Zone != ZoneRow || got.Row != i {
				t.Fatalf("y=%d: got %+v, want row %d", y, got, i)
			}
		}
	}
}

func TestHitSessionsRowsBeyondCount(t *testing.T) {
	// The gap between the last row and the banner belongs to nobody.
	last := SessRowBase - SessRowTopPad + SessRowsMax*SessRowStep
	if got := HitSessions(100, last); got.Zone != ZoneNone {
		t.Fatalf("below the last row: got %+v, want ZoneNone", got)
	}
	if got := HitSessions(100, BannerTop-1); got.Zone != ZoneNone {
		t.Fatalf("just above the banner: got %+v, want ZoneNone", got)
	}
}

func TestHitSessionsChrome(t *testing.T) {
	if got := HitSessions(60, 10); got.Zone != ZoneTitle {
		t.Fatalf("title: %+v", got)
	}
	if got := HitSessions(160, BannerTop+5); got.Zone != ZoneBanner {
		t.Fatalf("banner: %+v", got)
	}
	if got := HitSessions(SoundIconX+2, SoundIconY+2); got.Zone != ZoneSoundIcon {
		t.Fatalf("sound icon: %+v", got)
	}
}

func TestGeometryMatchesTheRenderer(t *testing.T) {
	// These numbers are the renderer's; ui.go aliases them, so a change here
	// must be a deliberate change of the screen layout.
	if ScreenW != 320 || ScreenH != 240 || HeaderH != 24 || BannerTop != 204 ||
		SessRowBase != 48 || SessRowStep != 22 || SessRowsMax != 7 || SessRowTopPad != 16 ||
		SoundIconX != 270 || SoundIconY != 4 || SoundIconW != 20 || SoundIconH != 16 || SpinnerX != 244 {
		t.Fatal("layout constants drifted from ui.go")
	}
}
