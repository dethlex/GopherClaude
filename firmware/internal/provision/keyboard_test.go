package provision

import "testing"

func TestPagesHaveTheExpectedKeys(t *testing.T) {
	lower := PageKeys(PageLower)
	if lower[0][0].Char != '1' || lower[0][9].Char != '0' {
		t.Fatalf("digits row: %+v", lower[0])
	}
	if lower[1][0].Char != 'q' || lower[1][9].Char != 'p' {
		t.Fatalf("qwerty row: %+v", lower[1])
	}
	if lower[2][0].Char != 'a' || lower[2][8].Char != 'l' || lower[2][9].Action != KeyBackspace {
		t.Fatalf("asdf row: %+v", lower[2])
	}
	if lower[3][0].Action != KeyShift || lower[3][1].Action != KeySymbols || lower[3][2].Char != 'z' ||
		lower[3][8].Char != 'm' || lower[3][9].Action != KeySpace {
		t.Fatalf("bottom row: %+v", lower[3])
	}
	upper := PageKeys(PageUpper)
	if upper[1][0].Char != 'Q' || upper[0][0].Char != '1' {
		t.Fatalf("upper page: %+v %+v", upper[1][0], upper[0][0])
	}
	sym := PageKeys(PageSymbols)
	if sym[0][0].Char != '!' || sym[3][0].Action != KeyLetters || sym[2][9].Action != KeyBackspace {
		t.Fatalf("symbols page: %+v %+v %+v", sym[0][0], sym[3][0], sym[2][9])
	}
	// Every printable character a WPA2 passphrase may need is reachable.
	want := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()-_=+[]{};:'\",.<>/?\\|~` "
	have := map[byte]bool{}
	for _, p := range []Page{PageLower, PageUpper, PageSymbols} {
		for _, row := range PageKeys(p) {
			for _, k := range row {
				if k.Action == KeyChar {
					have[k.Char] = true
				}
				if k.Action == KeySpace {
					have[' '] = true
				}
			}
		}
	}
	for i := 0; i < len(want); i++ {
		if !have[want[i]] {
			t.Errorf("character %q is not on any page", want[i])
		}
	}
}

func TestKeyAtGrid(t *testing.T) {
	k, row, col, ok := KeyAt(PageLower, 5, GridTop+5)
	if !ok || row != 0 || col != 0 || k.Char != '1' {
		t.Fatalf("top-left: %+v %d %d %v", k, row, col, ok)
	}
	k, row, col, ok = KeyAt(PageLower, 9*KeyW+KeyW/2, GridTop+3*KeyH+KeyH/2)
	if !ok || row != 3 || col != 9 || k.Action != KeySpace {
		t.Fatalf("bottom-right: %+v %d %d %v", k, row, col, ok)
	}
	if _, _, _, ok := KeyAt(PageLower, 100, GridTop-1); ok {
		t.Fatal("above the grid is not a key")
	}
	if _, _, _, ok := KeyAt(PageLower, 100, GridTop+Rows*KeyH); ok {
		t.Fatal("below the grid is not a key")
	}
}

func TestKeyAtEmptyCell(t *testing.T) {
	// The symbols page leaves cells empty; a tap there must be ignored.
	sym := PageKeys(PageSymbols)
	for col := 0; col < Cols; col++ {
		if sym[3][col].Action == KeyNone {
			if k, _, _, ok := KeyAt(PageSymbols, col*KeyW+2, GridTop+3*KeyH+2); ok {
				t.Fatalf("empty cell %d returned %+v", col, k)
			}
			return
		}
	}
	t.Fatal("symbols page has no empty cell on the bottom row")
}

func TestNextPage(t *testing.T) {
	shift := Key{Action: KeyShift}
	if NextPage(PageLower, shift) != PageUpper || NextPage(PageUpper, shift) != PageLower {
		t.Fatal("shift must toggle lower/upper")
	}
	if NextPage(PageLower, Key{Action: KeySymbols}) != PageSymbols {
		t.Fatal("?123 must open symbols")
	}
	if NextPage(PageSymbols, Key{Action: KeyLetters}) != PageLower {
		t.Fatal("abc must return to lower")
	}
	if NextPage(PageUpper, Key{Action: KeyChar, Char: 'A'}) != PageUpper {
		t.Fatal("typing must keep the page")
	}
}

func TestEditor(t *testing.T) {
	var e Editor
	for _, c := range "hunter2" {
		if !e.Apply(Key{Action: KeyChar, Char: byte(c)}) {
			t.Fatalf("char %q rejected", c)
		}
	}
	e.Apply(Key{Action: KeySpace})
	if e.Text() != "hunter2 " {
		t.Fatalf("text %q", e.Text())
	}
	e.Apply(Key{Action: KeyBackspace})
	e.Apply(Key{Action: KeyBackspace})
	if e.Text() != "hunter" {
		t.Fatalf("after backspace %q", e.Text())
	}
	if e.Apply(Key{Action: KeyShift}) {
		t.Fatal("shift must not change the text")
	}
	if e.Tail(4) != "nter" || e.Tail(10) != "hunter" {
		t.Fatalf("tail %q %q", e.Tail(4), e.Tail(10))
	}
	e.Set("abc")
	if e.Text() != "abc" {
		t.Fatalf("set %q", e.Text())
	}
	e.Reset()
	if e.Text() != "" || e.Apply(Key{Action: KeyBackspace}) {
		t.Fatal("reset / backspace on empty")
	}
}

func TestEditorStopsAtMaxText(t *testing.T) {
	var e Editor
	for i := 0; i < MaxText+5; i++ {
		e.Apply(Key{Action: KeyChar, Char: 'x'})
	}
	if len(e.Text()) != MaxText {
		t.Fatalf("length %d, want %d", len(e.Text()), MaxText)
	}
}

func TestFieldHit(t *testing.T) {
	if FieldHit(10, FieldTop+5) != FieldCancel {
		t.Fatal("left of the field is CANCEL")
	}
	if FieldHit(319, FieldTop+FieldH-1) != FieldOK {
		t.Fatal("right of the field is OK")
	}
	if FieldHit(160, FieldTop+5) != FieldNone {
		t.Fatal("the text itself is not a button")
	}
	if FieldHit(10, GridTop+5) != FieldNone {
		t.Fatal("the grid is not the field band")
	}
}
