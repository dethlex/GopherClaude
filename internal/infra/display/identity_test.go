package display

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	id1, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(id1.ID) != IDLen || len(id1.Token) != TokenLen {
		t.Fatalf("lengths %d %d", len(id1.ID), len(id1.Token))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v err %v", info.Mode(), err)
	}
	id2, err := LoadOrCreateIdentity(path)
	if err != nil || id2 != id1 {
		t.Fatalf("second load differs: %+v vs %+v (%v)", id2, id1, err)
	}
	other, _ := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "agent.json"))
	if other.ID == id1.ID {
		t.Fatal("two identities collided")
	}
}
