package provision

import "claudecontrol/firmware/internal/layout"

// MaxNetworks caps the list: a scan in a city block returns dozens and the
// user wants the strong ones.
const MaxNetworks = 32

// PageColX is where the page buttons start on the network list; rows end
// there.
const PageColX = 280

// Network is one access point from a scan.
type Network struct {
	SSID string
	RSSI int
}

// NetworkList is the de-duplicated, strongest-first scan result, paged like
// the session list.
type NetworkList struct {
	items []Network
	page  int
}

// NewNetworkList drops hidden networks, keeps the strongest entry per SSID,
// sorts by signal and caps the result.
func NewNetworkList(scan []Network) NetworkList {
	var items []Network
	for _, n := range scan {
		if n.SSID == "" {
			continue
		}
		dup := false
		for i := range items {
			if items[i].SSID == n.SSID {
				dup = true
				if n.RSSI > items[i].RSSI {
					items[i].RSSI = n.RSSI
				}

				break
			}
		}
		if !dup {
			items = append(items, n)
		}
	}

	// Insertion sort by RSSI descending: the list is tiny.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].RSSI < items[j].RSSI; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
	if len(items) > MaxNetworks {
		items = items[:MaxNetworks]
	}

	return NetworkList{items: items}
}

func (l *NetworkList) Len() int {
	return len(l.items)
}

// Rows returns the networks of the current page.
func (l *NetworkList) Rows() []Network {
	start := l.page * layout.SessRowsMax
	if start >= len(l.items) {
		return nil
	}
	end := start + layout.SessRowsMax
	if end > len(l.items) {
		end = len(l.items)
	}

	return l.items[start:end]
}

func (l *NetworkList) Page() int {
	return l.page
}

func (l *NetworkList) Pages() int {
	return (len(l.items) + layout.SessRowsMax - 1) / layout.SessRowsMax
}

func (l *NetworkList) Next() {
	if l.page+1 < l.Pages() {
		l.page++
	}
}

func (l *NetworkList) Prev() {
	if l.page > 0 {
		l.page--
	}
}

// Signal thresholds in dBm for the four-bar glyph.
const (
	rssiFourBars  = -55
	rssiThreeBars = -65
	rssiTwoBars   = -75
)

// Bars maps an RSSI to 1..4 signal bars.
func Bars(rssi int) int {
	switch {
	case rssi >= rssiFourBars:
		return 4
	case rssi >= rssiThreeBars:
		return 3
	case rssi >= rssiTwoBars:
		return 2
	}

	return 1
}

type ListAction uint8

const (
	ListNone ListAction = iota
	ListRow
	ListPageNext
	ListPagePrev
	ListRescan
	ListOther
)

// ListHit maps a tap on the network list screen: rows (left of the page
// column), the two page buttons stacked in that column, and RESCAN / OTHER
// in the bottom band.
func ListHit(x, y int) (ListAction, int) {
	if b := BandHit(x, y, 2); b == 0 {
		return ListRescan, 0
	} else if b == 1 {
		return ListOther, 0
	}

	rowTop := layout.SessRowBase - layout.SessRowTopPad
	rowsBottom := rowTop + layout.SessRowsMax*layout.SessRowStep
	if y < rowTop || y >= rowsBottom {
		return ListNone, 0
	}
	if x >= PageColX {
		if y < rowTop+(rowsBottom-rowTop)/2 {
			return ListPagePrev, 0
		}

		return ListPageNext, 0
	}

	return ListRow, (y - rowTop) / layout.SessRowStep
}
