package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	write(t, path, "first\nsecond\nthird\n")

	got, err := Tail(path, 8)
	if err != nil {
		t.Fatal(err)
	}

	// The last 8 bytes: the tail of "second" plus the whole "third" line.
	if string(got) != "d\nthird\n" {
		t.Errorf("Tail(8) = %q", got)
	}

	whole, err := Tail(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if string(whole) != "first\nsecond\nthird\n" {
		t.Errorf("Tail(big) = %q, want the whole file", whole)
	}

	if _, err := Tail(filepath.Join(t.TempDir(), "absent"), 8); err == nil {
		t.Error("Tail(missing) = nil error, want one")
	}
}

func TestCursorReadNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	write(t, path, "a\nb\n")

	var (
		cur    Cursor
		lines  []string
		resets int
	)

	onLine := func(line []byte) { lines = append(lines, strings.TrimSuffix(string(line), "\n")) }
	onReset := func() { resets++ }

	if err := cur.ReadNew(path, onLine, onReset); err != nil {
		t.Fatal(err)
	}

	if strings.Join(lines, ",") != "a,b" || cur.Offset != 4 {
		t.Fatalf("first pass: lines=%v offset=%d", lines, cur.Offset)
	}

	// Nothing new: no callbacks, offset unchanged.
	if err := cur.ReadNew(path, onLine, onReset); err != nil {
		t.Fatal(err)
	}

	if len(lines) != 2 || cur.Offset != 4 {
		t.Fatalf("idle pass: lines=%v offset=%d", lines, cur.Offset)
	}

	// A complete line and a torn one: the torn line waits, the offset stops
	// at its start.
	appendTo(t, path, "c\nd")

	if err := cur.ReadNew(path, onLine, onReset); err != nil {
		t.Fatal(err)
	}

	if strings.Join(lines, ",") != "a,b,c" || cur.Offset != 6 {
		t.Fatalf("torn pass: lines=%v offset=%d", lines, cur.Offset)
	}

	appendTo(t, path, "\n")

	if err := cur.ReadNew(path, onLine, onReset); err != nil {
		t.Fatal(err)
	}

	if strings.Join(lines, ",") != "a,b,c,d" || cur.Offset != 8 {
		t.Fatalf("completed pass: lines=%v offset=%d", lines, cur.Offset)
	}

	if resets != 0 {
		t.Fatalf("resets = %d before any rewrite", resets)
	}

	// The file shrank: onReset fires and the content is read from the start.
	write(t, path, "x\n")

	if err := cur.ReadNew(path, onLine, onReset); err != nil {
		t.Fatal(err)
	}

	if resets != 1 || lines[len(lines)-1] != "x" || cur.Offset != 2 {
		t.Errorf("rewrite pass: resets=%d lines=%v offset=%d", resets, lines, cur.Offset)
	}

	// A nil onReset is allowed.
	write(t, path, "")

	if err := cur.ReadNew(path, onLine, nil); err != nil {
		t.Fatal(err)
	}

	if cur.Offset != 0 {
		t.Errorf("offset after truncation to empty = %d, want 0", cur.Offset)
	}

	if err := cur.ReadNew(filepath.Join(t.TempDir(), "absent"), onLine, nil); err == nil {
		t.Error("ReadNew(missing) = nil error, want one")
	}
}
