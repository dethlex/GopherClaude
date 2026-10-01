package provision

import (
	"testing"

	"claudecontrol/firmware/internal/layout"
)

func TestNewNetworkListDedupsKeepingStrongest(t *testing.T) {
	l := NewNetworkList([]Network{
		{SSID: "Home", RSSI: -70},
		{SSID: "Cafe", RSSI: -40},
		{SSID: "Home", RSSI: -55}, // the same network on another band
		{SSID: "", RSSI: -30},     // hidden: never listed
		{SSID: "Far", RSSI: -88},
	})
	rows := l.Rows()
	if l.Len() != 3 || len(rows) != 3 {
		t.Fatalf("len %d rows %d", l.Len(), len(rows))
	}
	if rows[0].SSID != "Cafe" || rows[1].SSID != "Home" || rows[1].RSSI != -55 || rows[2].SSID != "Far" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestNetworkListPaging(t *testing.T) {
	var scan []Network
	for i := 0; i < 20; i++ {
		scan = append(scan, Network{SSID: string(rune('A' + i)), RSSI: -30 - i})
	}
	l := NewNetworkList(scan)
	if l.Pages() != 3 || l.Page() != 0 || len(l.Rows()) != layout.SessRowsMax {
		t.Fatalf("pages %d page %d rows %d", l.Pages(), l.Page(), len(l.Rows()))
	}
	l.Prev() // clamps
	if l.Page() != 0 {
		t.Fatal("prev on the first page moved")
	}
	l.Next()
	l.Next()
	if l.Page() != 2 || len(l.Rows()) != 6 || l.Rows()[0].SSID != "O" {
		t.Fatalf("last page: %d %+v", l.Page(), l.Rows())
	}
	l.Next() // clamps
	if l.Page() != 2 {
		t.Fatal("next on the last page moved")
	}
}

func TestNetworkListCap(t *testing.T) {
	var scan []Network
	for i := 0; i < MaxNetworks+10; i++ {
		scan = append(scan, Network{SSID: "n" + string(rune('a'+i%26)) + string(rune('a'+i/26)), RSSI: -i})
	}
	if l := NewNetworkList(scan); l.Len() != MaxNetworks {
		t.Fatalf("len %d", l.Len())
	}
}

func TestBars(t *testing.T) {
	cases := map[int]int{-30: 4, -55: 4, -56: 3, -65: 3, -66: 2, -75: 2, -76: 1, -100: 1}
	for rssi, want := range cases {
		if got := Bars(rssi); got != want {
			t.Errorf("Bars(%d) = %d, want %d", rssi, got, want)
		}
	}
}

func TestListHit(t *testing.T) {
	rowTop := layout.SessRowBase - layout.SessRowTopPad
	if a, i := ListHit(100, rowTop+layout.SessRowStep*2+5); a != ListRow || i != 2 {
		t.Fatalf("row 2: %v %d", a, i)
	}
	if a, _ := ListHit(PageColX+10, rowTop+10); a != ListPagePrev {
		t.Fatalf("upper page button: %v", a)
	}
	if a, _ := ListHit(PageColX+10, rowTop+layout.SessRowStep*5); a != ListPageNext {
		t.Fatalf("lower page button: %v", a)
	}
	if a, _ := ListHit(50, BandTop+10); a != ListRescan {
		t.Fatalf("left band button: %v", a)
	}
	if a, _ := ListHit(250, BandTop+10); a != ListOther {
		t.Fatalf("right band button: %v", a)
	}
	if a, _ := ListHit(100, 10); a != ListNone {
		t.Fatalf("title: %v", a)
	}
}
