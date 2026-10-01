package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func TestRotatesAtSizeAndKeepsGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes: two per 100-byte file, the third rotates
	for i := 0; i < 7; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	// 7 lines: files hold 2+2+2+1 → live (1 line), .1 (2), .2 (2); the oldest pair is gone.
	live, _ := os.ReadFile(path)
	if len(live) != len(line) {
		t.Fatalf("live file %d bytes, want %d", len(live), len(line))
	}
	for _, g := range []string{path + ".1", path + ".2"} {
		data, err := os.ReadFile(g)
		if err != nil || len(data) != 2*len(line) {
			t.Fatalf("%s: %d bytes, err %v", g, len(data), err)
		}
	}
	if exists(path + ".3") {
		t.Fatal("generation past keep must be dropped")
	}
}

func TestOversizedWriteStillLands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	big := []byte(strings.Repeat("y", 50))
	if _, err := w.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(big); err != nil {
		t.Fatal(err)
	}
	live, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if len(live) != len(big) || len(old) != len(big) {
		t.Fatalf("live %d old %d", len(live), len(old))
	}
}

func TestReopenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, _ := Open(path, 1000, 1)
	w.Write([]byte("first\n"))
	w.Close()
	w, _ = Open(path, 1000, 1)
	w.Write([]byte("second\n"))
	w.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "first\nsecond\n" {
		t.Fatalf("%q", data)
	}
}

func TestKeepZeroTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, _ := Open(path, 20, 0)
	defer w.Close()
	w.Write([]byte(strings.Repeat("a", 15)))
	w.Write([]byte(strings.Repeat("b", 15)))
	data, _ := os.ReadFile(path)
	if string(data) != strings.Repeat("b", 15) || exists(path+".1") {
		t.Fatalf("%q, .1 exists=%v", data, exists(path+".1"))
	}
}

func TestOpenRejectsBadLimits(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "x"), 0, 1); err == nil {
		t.Fatal("max 0 accepted")
	}
}
