package lockfs

import (
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// lsof -F pn over a lock directory, in the real shape (a descriptor line
// precedes every name): pid 34530 holds two locks plus a dotfile, pid 4242
// holds one lock through two descriptors, a shell (99999) sits in the dir.
const lsofLocks = "p34530\n" +
	"f35u\n" +
	"n/Users/x/locks/.coordination.lock\n" +
	"f36u\n" +
	"n/Users/x/locks/desk.lock\n" +
	"f37u\n" +
	"n/Users/x/locks/review.lock\n" +
	"p4242\n" +
	"f12u\n" +
	"n/Users/x/locks/term.lock\n" +
	"f13u\n" +
	"n/Users/x/locks/term.lock\n" +
	"p99999\n" +
	"fcwd\n" +
	"n/Users/x/locks\n"

func TestHolders(t *testing.T) {
	var gotArgs []string

	l := &Lister{Dir: "/Users/x/locks", Run: func(args ...string) ([]byte, error) {
		gotArgs = args

		return []byte(lsofLocks), nil
	}}

	got, err := l.Holders()
	if err != nil {
		t.Fatal(err)
	}

	want := []Holder{{PID: 4242, ID: "term"}, {PID: 34530, ID: "desk"}, {PID: 34530, ID: "review"}}
	if len(got) != len(want) {
		t.Fatalf("Holders = %+v, want %+v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Holders[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if len(gotArgs) != 4 || gotArgs[0] != "-F" || gotArgs[1] != "pn" || gotArgs[2] != "+D" || gotArgs[3] != "/Users/x/locks" {
		t.Errorf("lsof args = %v", gotArgs)
	}
}

func TestCWDs(t *testing.T) {
	var gotArgs []string

	l := &Lister{Dir: "/x", Run: func(args ...string) ([]byte, error) {
		gotArgs = args

		return []byte("p11238\nfcwd\nn/Users/x/proj-a\np18889\nfcwd\nn/Users/x/proj-b\n"), nil
	}}

	got, err := l.CWDs([]int{11238, 18889})
	if err != nil {
		t.Fatal(err)
	}

	if got[11238] != "/Users/x/proj-a" || got[18889] != "/Users/x/proj-b" {
		t.Errorf("CWDs = %v", got)
	}

	if len(gotArgs) != 7 || gotArgs[3] != "-p" || gotArgs[4] != "11238,18889" {
		t.Errorf("lsof args = %v", gotArgs)
	}

	// No pids: no lsof call at all.
	calls := 0
	l.Run = func(...string) ([]byte, error) { calls++; return nil, nil }

	if got, err := l.CWDs(nil); err != nil || len(got) != 0 || calls != 0 {
		t.Errorf("CWDs(nil) = %v, %v, calls=%d", got, err, calls)
	}
}

func TestRegistryCachesAndRefreshes(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Second

	lsofCalls, buildCalls := 0, 0

	r := NewRegistry("/Users/x/locks", ttl, func(l *Lister, holders []Holder) ([]domain.Session, error) {
		buildCalls++

		sessions := make([]domain.Session, 0, len(holders))
		for _, h := range holders {
			sessions = append(sessions, domain.Session{ID: h.ID, PID: h.PID})
		}

		return sessions, nil
	})
	r.Now = func() time.Time { return now }
	r.Run = func(...string) ([]byte, error) {
		lsofCalls++

		return []byte(lsofLocks), nil
	}

	sessions, err := r.Sessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 3 || sessions[0].ID != "term" || sessions[0].PID != 4242 {
		t.Fatalf("sessions = %+v", sessions)
	}

	if _, err := r.Sessions(); err != nil {
		t.Fatal(err)
	}

	if lsofCalls != 1 || buildCalls != 1 {
		t.Errorf("within TTL: lsof=%d build=%d, want 1/1", lsofCalls, buildCalls)
	}

	now = now.Add(ttl + time.Second)

	if _, err := r.Sessions(); err != nil {
		t.Fatal(err)
	}

	if lsofCalls != 2 || buildCalls != 2 {
		t.Errorf("after TTL: lsof=%d build=%d, want 2/2", lsofCalls, buildCalls)
	}
}

func TestRegistryEmptyResultIsCached(t *testing.T) {
	calls := 0

	r := NewRegistry("/x", time.Minute, func(*Lister, []Holder) ([]domain.Session, error) { return nil, nil })
	r.Run = func(...string) ([]byte, error) { calls++; return nil, nil }

	for i := 0; i < 2; i++ {
		sessions, err := r.Sessions()
		if err != nil || len(sessions) != 0 {
			t.Fatalf("Sessions = %v, %v", sessions, err)
		}
	}

	if calls != 1 {
		t.Errorf("lsof calls = %d, want 1 (an empty result is cached too)", calls)
	}
}
