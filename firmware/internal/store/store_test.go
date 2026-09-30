package store

import (
	"bytes"
	"testing"

	"claudecontrol/firmware/internal/touchcal"
)

func sample() Record {
	var r Record
	copy(r.SSID[:], "Home")
	r.SSIDLen = 4
	copy(r.Password[:], "hunter2-hunter2")
	r.PasswordLen = 15
	r.WiFiVerified = true
	r.WiFiAttempts = 2
	for i := range r.DeviceID {
		r.DeviceID[i] = byte(i)
	}
	for i := range r.Secret {
		r.Secret[i] = byte(0xA0 + i)
	}
	r.HasIdentity = true
	r.AgentAddrs[0] = [4]byte{192, 168, 31, 190}
	r.AgentPort = 7070
	r.AgentID[15] = 0x7f
	r.Paired = true
	r.Cal = touchcal.Cal{Swap: true, X0: 3600, X1: 300, Y0: 320, Y1: 3580, Valid: true}
	r.SoundOff = true
	return r
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := sample()
	b := Encode(want, 7)
	if len(b) != RecordSize || RecordSize > SectorSize {
		t.Fatalf("record size %d", len(b))
	}
	got, gen, ok := Decode(b[:])
	if !ok || gen != 7 {
		t.Fatalf("decode ok=%v gen=%d", ok, gen)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	b := Encode(sample(), 1)
	flipped := b
	flipped[RecordSize-1] ^= 0x01 // last payload byte
	if _, _, ok := Decode(flipped[:]); ok {
		t.Fatal("flipped payload byte accepted")
	}
	badMagic := b
	badMagic[0] = 'X'
	if _, _, ok := Decode(badMagic[:]); ok {
		t.Fatal("bad magic accepted")
	}
	erased := bytes.Repeat([]byte{0xFF}, SectorSize)
	if _, _, ok := Decode(erased); ok {
		t.Fatal("erased sector accepted")
	}
	if _, _, ok := Decode(b[:RecordSize-10]); ok {
		t.Fatal("short buffer accepted")
	}
}

func TestNewestPicksHigherGeneration(t *testing.T) {
	old := sample()
	old.SoundOff = false
	a := Encode(old, 3)
	b := Encode(sample(), 4)
	rec, slot, gen, ok := Newest(a[:], b[:])
	if !ok || slot != 1 || gen != 4 || !rec.SoundOff {
		t.Fatalf("got slot=%d gen=%d ok=%v rec.SoundOff=%v", slot, gen, ok, rec.SoundOff)
	}
	rec, slot, gen, ok = Newest(b[:], a[:])
	if !ok || slot != 0 || gen != 4 || !rec.SoundOff {
		t.Fatalf("swapped: slot=%d gen=%d ok=%v", slot, gen, ok)
	}
}

func TestNewestSurvivesOneBrokenSlot(t *testing.T) {
	good := Encode(sample(), 9)
	erased := bytes.Repeat([]byte{0xFF}, SectorSize)
	if _, slot, gen, ok := Newest(erased, good[:]); !ok || slot != 1 || gen != 9 {
		t.Fatalf("erased A: slot=%d gen=%d ok=%v", slot, gen, ok)
	}
	if _, slot, _, ok := Newest(good[:], erased); !ok || slot != 0 {
		t.Fatalf("erased B: slot=%d ok=%v", slot, ok)
	}
	if _, slot, _, ok := Newest(erased, erased); ok || slot != -1 {
		t.Fatalf("both erased: slot=%d ok=%v", slot, ok)
	}
}

func TestNewestPrefersSlotBOnTie(t *testing.T) {
	a := Encode(sample(), 5)
	b := Encode(sample(), 5)
	if _, slot, _, ok := Newest(a[:], b[:]); !ok || slot != 1 {
		t.Fatalf("tie: slot=%d ok=%v", slot, ok)
	}
}

func TestNextSlotOverwritesTheOlderOrBroken(t *testing.T) {
	older := Encode(sample(), 2)
	newer := Encode(sample(), 3)
	erased := bytes.Repeat([]byte{0xFF}, SectorSize)
	if s := NextSlot(older[:], newer[:]); s != 0 {
		t.Fatalf("older A: %d", s)
	}
	if s := NextSlot(newer[:], older[:]); s != 1 {
		t.Fatalf("older B: %d", s)
	}
	if s := NextSlot(erased, newer[:]); s != 0 {
		t.Fatalf("erased A: %d", s)
	}
	if s := NextSlot(newer[:], erased); s != 1 {
		t.Fatalf("erased B: %d", s)
	}
	if s := NextSlot(erased, erased); s != 0 {
		t.Fatalf("both erased: %d", s)
	}
}

func TestLengthsAreClamped(t *testing.T) {
	r := sample()
	r.SSIDLen = 200
	r.PasswordLen = 200
	b := Encode(r, 1)
	got, _, ok := Decode(b[:])
	if !ok || got.SSIDLen != 32 || got.PasswordLen != 64 {
		t.Fatalf("lengths not clamped: %d %d", got.SSIDLen, got.PasswordLen)
	}
}
