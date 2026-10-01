package display

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/dethlex/GopherClaude/internal/infra/atomicfile"
)

// Device is a paired display as the agent remembers it.
type Device struct {
	ID       string    `json:"device_id"`
	Name     string    `json:"name,omitempty"`
	Token    string    `json:"device_token"`
	LastAddr string    `json:"last_addr,omitempty"`
	PairedAt time.Time `json:"paired_at"`
}

// PairStore is devices.json: which displays may connect and with what
// token. It is small and rewritten whole on every change.
type PairStore struct {
	path string

	mu      sync.Mutex
	devices map[string]Device
}

// LoadPairStore reads the store; a missing file is an empty store.
func LoadPairStore(path string) (*PairStore, error) {
	p := &PairStore{path: path, devices: map[string]Device{}}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var list []Device
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, d := range list {
			p.devices[d.ID] = d
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}

	return p, nil
}

// Token returns the pairing token of a known device.
func (p *PairStore) Token(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	d, ok := p.devices[id]

	return d.Token, ok
}

// Upsert adds or updates a device, keeping the address last seen when the
// caller did not provide one.
func (p *PairStore) Upsert(d Device) error {
	p.mu.Lock()
	if old, ok := p.devices[d.ID]; ok && d.LastAddr == "" {
		d.LastAddr = old.LastAddr
	}
	p.devices[d.ID] = d
	p.mu.Unlock()

	return p.save()
}

// Touch records where a device connected from.
func (p *PairStore) Touch(id, addr string) error {
	p.mu.Lock()
	d, ok := p.devices[id]
	if ok {
		d.LastAddr = addr
		p.devices[id] = d
	}
	p.mu.Unlock()

	if !ok {
		return nil
	}

	return p.save()
}

func (p *PairStore) Forget(id string) error {
	p.mu.Lock()
	delete(p.devices, id)
	p.mu.Unlock()

	return p.save()
}

// Devices lists the known devices sorted by id.
func (p *PairStore) Devices() []Device {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.sortedLocked()
}

func (p *PairStore) sortedLocked() []Device {
	list := make([]Device, 0, len(p.devices))
	for _, d := range p.devices {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	return list
}

func (p *PairStore) save() error {
	p.mu.Lock()
	list := p.sortedLocked()
	p.mu.Unlock()

	return atomicfile.WriteJSON(p.path, list, stateFileMode)
}
