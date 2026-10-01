package display

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	p, err := LoadPairStore(path) // absent file: empty store
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Token("nobody"); ok {
		t.Fatal("empty store knows a device")
	}
	d := Device{ID: "0123456789abcdef0123456789abcdef", Name: "Kitchen", Token: "t0", PairedAt: time.Unix(1700000000, 0).UTC()}
	if err := p.Upsert(d); err != nil {
		t.Fatal(err)
	}
	if err := p.Touch(d.ID, "192.168.31.26:50002"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}

	q, err := LoadPairStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, ok := q.Token(d.ID)
	if !ok || tok != "t0" {
		t.Fatalf("token %q %v", tok, ok)
	}
	devs := q.Devices()
	if len(devs) != 1 || devs[0].Name != "Kitchen" || devs[0].LastAddr != "192.168.31.26:50002" || !devs[0].PairedAt.Equal(d.PairedAt) {
		t.Fatalf("devices %+v", devs)
	}
	if err := q.Forget(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Token(d.ID); ok {
		t.Fatal("forgotten device still known")
	}
	r, _ := LoadPairStore(path)
	if len(r.Devices()) != 0 {
		t.Fatal("forget was not persisted")
	}
}

func TestPairStoreUpsertKeepsLastAddr(t *testing.T) {
	p, _ := LoadPairStore(filepath.Join(t.TempDir(), "devices.json"))
	p.Upsert(Device{ID: "a", Token: "t1"})
	p.Touch("a", "10.0.0.5:1")
	p.Upsert(Device{ID: "a", Token: "t2", Name: "renamed"})
	d := p.Devices()[0]
	if d.Token != "t2" || d.Name != "renamed" || d.LastAddr != "10.0.0.5:1" {
		t.Fatalf("%+v", d)
	}
}
