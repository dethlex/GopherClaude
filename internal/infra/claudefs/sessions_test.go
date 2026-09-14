package claudefs

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSessionFile(t *testing.T, dir, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRegistry(t *testing.T) {
	dir := t.TempDir()

	writeSessionFile(t, dir, "100.json",
		`{"pid":100,"sessionId":"alive","cwd":"/Users/x/proj","kind":"interactive","startedAt":1781198322264,"name":"proj-0c","nameSource":"derived"}`)
	writeSessionFile(t, dir, "200.json",
		`{"pid":200,"sessionId":"dead","cwd":"/Users/x/proj","kind":"interactive","startedAt":1}`)
	writeSessionFile(t, dir, "300.json",
		`{"pid":300,"sessionId":"batch","cwd":"/Users/x/proj","kind":"background","startedAt":1}`)
	writeSessionFile(t, dir, "400.json",
		`{"pid":400,"sessionId":"alive","cwd":"/Users/x/proj","kind":"interactive","startedAt":1}`)
	writeSessionFile(t, dir, "junk.json", "not json")

	registry := NewSessionRegistry(dir, discardLogger())
	registry.alive = func(pid int) bool { return pid == 100 || pid == 300 || pid == 400 }

	sessions, err := registry.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("Sessions() returned %d, want 1 (alive, interactive, deduped)", len(sessions))
	}

	if sessions[0].ID != "alive" || sessions[0].Dir != "/Users/x/proj" || sessions[0].Name != "proj-0c" {
		t.Errorf("Sessions()[0] = %+v, want the registry name proj-0c", sessions[0])
	}
}

func TestSessionRegistryMissingDir(t *testing.T) {
	registry := NewSessionRegistry(filepath.Join(t.TempDir(), "absent"), discardLogger())

	sessions, err := registry.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error = %v, want nil for a missing dir", err)
	}

	if sessions != nil {
		t.Errorf("Sessions() = %v, want nil", sessions)
	}
}
