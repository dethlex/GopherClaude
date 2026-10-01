package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const (
	devA = "0123456789abcdef0123456789abcdef"
	devB = "fedcba9876543210fedcba9876543210"
	agX  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	agY  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tokX = "1111111111111111111111111111111111111111111111111111111111111111"
	tokY = "2222222222222222222222222222222222222222222222222222222222222222"
	secA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	secB = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func TestStoreOpenMissingIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.View(func(st *State) {
		if st.Devices == nil || st.Agents == nil || len(st.Pairs) != 0 {
			t.Fatalf("empty state must have usable maps: %+v", st)
		}
	})
}

func TestStoreUpdatePersistsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, _ := Open(path)
	err := s.Update(func(st *State) error {
		st.Devices[devA] = &Device{ID: devA, Secret: secA, LastSeen: t0, Name: "Kitchen"}
		st.Agents[agX] = &Agent{ID: agX, Token: tokX, Addrs: []string{"192.168.1.10"}, Port: 7070, LastSeen: t0}
		st.AddPair(devA, agX, t0)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r.View(func(st *State) {
		d := st.Devices[devA]
		if d == nil || d.Name != "Kitchen" || !d.LastSeen.Equal(t0) || d.Secret != secA {
			t.Fatalf("device %+v", d)
		}
		if a := st.Agents[agX]; a == nil || a.Port != 7070 || len(a.Addrs) != 1 {
			t.Fatalf("agent %+v", a)
		}
		if !st.IsPaired(devA, agX) || st.IsPaired(devA, agY) {
			t.Fatal("pairs not persisted")
		}
	})
}

func TestStoreUpdateErrorDoesNotSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	s.Update(func(st *State) error {
		st.Devices[devA] = &Device{ID: devA}

		return os.ErrInvalid
	})
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a failed update must not write the file")
	}
	// The in-memory change is kept: callers return errors for HTTP statuses,
	// not to roll back, so they must not mutate before deciding.
}

func TestStatePairQueries(t *testing.T) {
	st := &State{Devices: map[string]*Device{}, Agents: map[string]*Agent{}}
	st.Devices[devA] = &Device{ID: devA}
	st.Devices[devB] = &Device{ID: devB}
	st.Agents[agX] = &Agent{ID: agX, Token: tokX, LastSeen: t0}
	st.Agents[agY] = &Agent{ID: agY, Token: tokY, LastSeen: t0.Add(time.Minute)}

	if !st.AddPair(devA, agX, t0) || st.AddPair(devA, agX, t0) {
		t.Fatal("AddPair must report a duplicate")
	}
	st.AddPair(devA, agY, t0)
	st.AddPair(devB, agX, t0)

	if ags := st.PairedAgents(devA); len(ags) != 2 || ags[0].ID != agY || ags[1].ID != agX {
		t.Fatalf("paired agents (newest first) %+v", ags)
	}
	if devs := st.PairedDevices(agX); len(devs) != 2 || devs[0].ID != devA || devs[1].ID != devB {
		t.Fatalf("paired devices %+v", devs)
	}
	if !st.HasPairs(devA) {
		t.Fatal("HasPairs")
	}
	if !st.RemovePair(devA, agY) || st.RemovePair(devA, agY) {
		t.Fatal("RemovePair must report absence")
	}
	if ags := st.PairedAgents(devA); len(ags) != 1 || ags[0].ID != agX {
		t.Fatalf("after remove %+v", ags)
	}
	if st.AgentByToken(tokY) == nil || st.AgentByToken(tokY).ID != agY || st.AgentByToken("nope") != nil {
		t.Fatal("AgentByToken")
	}
}

func TestStorePurge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	old := t0.Add(-91 * 24 * time.Hour)
	s.Update(func(st *State) error {
		st.Devices[devA] = &Device{ID: devA, LastSeen: old}              // stale, unpaired: goes
		st.Devices[devB] = &Device{ID: devB, LastSeen: old}              // stale but paired: stays
		st.Devices["c"] = &Device{ID: "c", LastSeen: t0.Add(-time.Hour)} // fresh: stays
		st.Agents[agX] = &Agent{ID: agX, LastSeen: old}                  // stale but paired: stays
		st.Agents[agY] = &Agent{ID: agY, LastSeen: old}                  // stale, unpaired: goes
		st.AddPair(devB, agX, old)

		return nil
	})
	n, err := s.Purge(t0, 90*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("purged %d err %v", n, err)
	}
	r, _ := Open(path)
	r.View(func(st *State) {
		if st.Devices[devA] != nil || st.Agents[agY] != nil {
			t.Fatal("stale unpaired entries must be gone from disk")
		}
		if st.Devices[devB] == nil || st.Devices["c"] == nil || st.Agents[agX] == nil {
			t.Fatal("paired or fresh entries must stay")
		}
	})
	if n, _ := s.Purge(t0, 90*24*time.Hour); n != 0 {
		t.Fatal("second purge must be a no-op")
	}
}
