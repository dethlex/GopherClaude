// Package provision holds the hardware-free half of the display's first-run
// and settings screens: the keyboard, the network list, every button
// rectangle and the boot policy. The firmware draws and reads the panel;
// this package decides what a tap at (x, y) means.
package provision

// Keyboard geometry: a 10×4 grid of 32×40 px keys under the title and the
// text field; 32 px is the smallest key a finger hits on this panel.
const (
	Cols    = 10
	Rows    = 4
	KeyW    = 32
	KeyH    = 40
	GridTop = 80

	// Text field band: CANCEL | text | OK.
	FieldTop = 32
	FieldH   = 36
	CancelW  = 64
	OKW      = 64

	// MaxText is the password limit of the settings record.
	MaxText = 64
)

type Page uint8

const (
	PageLower Page = iota
	PageUpper
	PageSymbols
)

type KeyAction uint8

const (
	KeyNone KeyAction = iota
	KeyChar
	KeyBackspace
	KeyShift
	KeySymbols
	KeyLetters
	KeySpace
)

// Key is one cell of a page; Label is what is drawn, Char what is typed.
type Key struct {
	Label  string
	Char   byte
	Action KeyAction
}

var (
	keyBackspace = Key{Label: "<-", Action: KeyBackspace}
	keyShift     = Key{Label: "SH", Action: KeyShift}
	keySymbols   = Key{Label: "?1", Action: KeySymbols}
	keyLetters   = Key{Label: "ab", Action: KeyLetters}
	keySpace     = Key{Label: "SP", Action: KeySpace}

	pageLower   = buildPage("1234567890", "qwertyuiop", "asdfghjkl", "zxcvbnm")
	pageUpper   = buildPage("1234567890", "QWERTYUIOP", "ASDFGHJKL", "ZXCVBNM")
	pageSymbols = buildSymbols()
)

// buildPage lays out a letters page: digits, two letter rows with backspace
// closing the third, then shift, ?123, seven letters and space.
func buildPage(r0, r1, r2, r3 string) [Rows][Cols]Key {
	var p [Rows][Cols]Key
	for i := 0; i < Cols; i++ {
		p[0][i] = charKey(r0[i])
		p[1][i] = charKey(r1[i])
	}
	for i := 0; i < len(r2); i++ {
		p[2][i] = charKey(r2[i])
	}
	p[2][Cols-1] = keyBackspace
	p[3][0] = keyShift
	p[3][1] = keySymbols
	for i := 0; i < len(r3); i++ {
		p[3][2+i] = charKey(r3[i])
	}
	p[3][Cols-1] = keySpace

	return p
}

func buildSymbols() [Rows][Cols]Key {
	var p [Rows][Cols]Key
	rows := [3]string{"!@#$%^&*()", "-_=+[]{};:", "'\",.<>/?\\"}
	for r, s := range rows {
		for i := 0; i < len(s); i++ {
			p[r][i] = charKey(s[i])
		}
	}
	p[2][Cols-1] = keyBackspace
	p[3][0] = keyLetters
	p[3][1] = charKey('|')
	p[3][2] = charKey('~')
	p[3][3] = charKey('`')
	p[3][Cols-1] = keySpace

	return p
}

func charKey(c byte) Key {
	return Key{Label: string(rune(c)), Char: c, Action: KeyChar}
}

// PageKeys returns the layout of a page for rendering.
func PageKeys(p Page) *[Rows][Cols]Key {
	switch p {
	case PageUpper:
		return &pageUpper
	case PageSymbols:
		return &pageSymbols
	}

	return &pageLower
}

// KeyAt maps a point to the key under it; ok is false outside the grid and
// on empty cells.
func KeyAt(p Page, x, y int) (key Key, row, col int, ok bool) {
	if y < GridTop || y >= GridTop+Rows*KeyH || x < 0 || x >= Cols*KeyW {
		return Key{}, 0, 0, false
	}
	row, col = (y-GridTop)/KeyH, x/KeyW
	key = PageKeys(p)[row][col]

	return key, row, col, key.Action != KeyNone
}

// NextPage is the page to show after a key: shift toggles case, ?123 and
// abc switch between symbols and letters, typing keeps the page.
func NextPage(p Page, k Key) Page {
	switch k.Action {
	case KeyShift:
		if p == PageUpper {
			return PageLower
		}

		return PageUpper
	case KeySymbols:
		return PageSymbols
	case KeyLetters:
		return PageLower
	}

	return p
}

// Editor is the text under construction, capped at the record's limit.
type Editor struct {
	buf [MaxText]byte
	n   int
}

// Apply feeds a key to the editor and reports whether the text changed.
func (e *Editor) Apply(k Key) bool {
	switch k.Action {
	case KeyChar, KeySpace:
		if e.n >= MaxText {
			return false
		}
		c := k.Char
		if k.Action == KeySpace {
			c = ' '
		}
		e.buf[e.n] = c
		e.n++

		return true
	case KeyBackspace:
		if e.n == 0 {
			return false
		}
		e.n--

		return true
	}

	return false
}

func (e *Editor) Text() string {
	return string(e.buf[:e.n])
}

// Tail is the last maxChars characters: the field shows the end of a long
// password, where the user is typing.
func (e *Editor) Tail(maxChars int) string {
	if e.n <= maxChars {
		return e.Text()
	}

	return string(e.buf[e.n-maxChars : e.n])
}

func (e *Editor) Reset() {
	e.n = 0
}

func (e *Editor) Set(s string) {
	e.n = copy(e.buf[:], s)
}

type FieldAction uint8

const (
	FieldNone FieldAction = iota
	FieldOK
	FieldCancel
)

// FieldHit maps a point in the text-field band to its buttons.
func FieldHit(x, y int) FieldAction {
	if y < FieldTop || y >= FieldTop+FieldH {
		return FieldNone
	}
	switch {
	case x < CancelW:
		return FieldCancel
	case x >= Cols*KeyW-OKW:
		return FieldOK
	}

	return FieldNone
}
