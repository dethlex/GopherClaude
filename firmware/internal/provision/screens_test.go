package provision

import (
	"testing"

	"claudecontrol/firmware/internal/layout"
)

func TestBandHit(t *testing.T) {
	if BandHit(10, BandTop+5, 2) != 0 || BandHit(310, BandTop+5, 2) != 1 {
		t.Fatal("two buttons split the band in halves")
	}
	if BandHit(100, BandTop+5, 3) != 0 || BandHit(160, BandTop+5, 3) != 1 || BandHit(300, BandTop+5, 3) != 2 {
		t.Fatal("three buttons split the band in thirds")
	}
	if BandHit(100, BandTop-1, 2) != -1 {
		t.Fatal("above the band is no button")
	}
	if !ConnectHit(160, BandTop+10) || ConnectHit(160, 100) {
		t.Fatal("ConnectHit is the whole band")
	}
}

func TestMenuHit(t *testing.T) {
	rowTop := layout.SessRowBase - layout.SessRowTopPad
	for i := range MenuItems {
		if item, ok := MenuHit(100, rowTop+i*layout.SessRowStep+3); !ok || item != i {
			t.Fatalf("item %d: %d %v", i, item, ok)
		}
	}
	if _, ok := MenuHit(100, rowTop+len(MenuItems)*layout.SessRowStep+3); ok {
		t.Fatal("below the last item is nothing")
	}
	if MenuItems[MenuBack] != "BACK" || MenuItems[MenuWiFi] != "WI-FI" {
		t.Fatalf("menu labels: %v", MenuItems)
	}
}

func TestNextBootStepPolicy(t *testing.T) {
	cases := []struct {
		name          string
		hasNet, verif bool
		attempts      uint8
		want          BootStep
	}{
		{"fresh device", false, false, 0, StepProvision},
		{"network typed, first boot", true, false, 0, StepConnect},
		{"unverified, two failed boots", true, false, 2, StepConnect},
		{"unverified, three failed boots", true, false, 3, StepProvision},
		{"verified network, router down for ten boots", true, true, 10, StepConnect},
	}
	for _, c := range cases {
		if got := NextBootStep(c.hasNet, c.verif, c.attempts); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
