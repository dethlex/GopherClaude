package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONCreatesModeAndLeavesNoTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "state.json")
	if err := WriteJSON(path, map[string]int{"a": 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode(), err)
	}
	var got map[string]int
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil || got["a"] != 1 {
		t.Fatalf("content %q err %v", data, err)
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("file must end with a newline")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestWriteJSONReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	WriteJSON(path, []int{1, 2, 3}, 0o644)
	if err := WriteJSON(path, []int{9}, 0o644); err != nil {
		t.Fatal(err)
	}
	var got []int
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &got)
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("%v", got)
	}
}

func TestWriteJSONRejectsUnmarshalable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := WriteJSON(path, make(chan int), 0o600); err == nil {
		t.Fatal("channels cannot be JSON")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("no file may be created when encoding fails")
	}
}
