// Package hub is the pairing and directory service: it registers agents and
// displays, matches a pairing code to a display, hands displays their agents'
// LAN addresses and keeps the latest device metrics. Frames never pass
// through it.
package hub

import (
	"crypto/subtle"
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

const stateFileMode = 0o600

// Metrics is what a display reports in its heartbeat; only the latest set
// is kept.
type Metrics struct {
	FW         string `json:"fw,omitempty"`
	UptimeS    int64  `json:"uptime_s"`
	HeapFree   int    `json:"heap_free"`
	BatteryMV  int    `json:"battery_mv"`
	RSSI       int    `json:"rssi"`
	Reconnects int    `json:"reconnects"`
	LastError  string `json:"last_error,omitempty"`
}

// Device is a display as the Hub knows it. Secret is the HMAC key it
// registered on first contact; Nonce is what its next heartbeat must sign;
// Code is the pairing code it currently shows (empty once paired).
type Device struct {
	ID           string    `json:"device_id"`
	Secret       string    `json:"secret"`
	Name         string    `json:"name,omitempty"`
	PublicIP     string    `json:"public_ip,omitempty"`
	Addr         string    `json:"addr,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	RegisteredAt time.Time `json:"registered_at"`
	Nonce        string    `json:"nonce,omitempty"`
	Code         string    `json:"code,omitempty"`
	CodeAt       time.Time `json:"code_at,omitempty"`
	Metrics      Metrics   `json:"metrics"`
}

// Agent is a host agent: its bearer token and where displays can dial it.
type Agent struct {
	ID           string    `json:"agent_id"`
	Token        string    `json:"token"`
	Addrs        []string  `json:"addrs"`
	Port         int       `json:"port"`
	Version      string    `json:"version,omitempty"`
	PublicIP     string    `json:"public_ip,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	RegisteredAt time.Time `json:"registered_at"`
}

type Pair struct {
	DeviceID string    `json:"device_id"`
	AgentID  string    `json:"agent_id"`
	PairedAt time.Time `json:"paired_at"`
}

// State is the whole Hub database; it is small and rewritten whole.
type State struct {
	Devices map[string]*Device `json:"devices"`
	Agents  map[string]*Agent  `json:"agents"`
	Pairs   []Pair             `json:"pairs"`
}

func newState() State {
	return State{Devices: map[string]*Device{}, Agents: map[string]*Agent{}}
}

// PairedAgents lists the agents a device is paired with, the most recently
// seen first: the display dials them in this order.
func (st *State) PairedAgents(deviceID string) []*Agent {
	var out []*Agent
	for _, p := range st.Pairs {
		if p.DeviceID != deviceID {
			continue
		}
		if a := st.Agents[p.AgentID]; a != nil {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })

	return out
}

// PairedDevices lists the devices paired with an agent, sorted by id.
func (st *State) PairedDevices(agentID string) []*Device {
	var out []*Device
	for _, p := range st.Pairs {
		if p.AgentID != agentID {
			continue
		}
		if d := st.Devices[p.DeviceID]; d != nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func (st *State) IsPaired(deviceID, agentID string) bool {
	for _, p := range st.Pairs {
		if p.DeviceID == deviceID && p.AgentID == agentID {
			return true
		}
	}

	return false
}

func (st *State) HasPairs(deviceID string) bool {
	for _, p := range st.Pairs {
		if p.DeviceID == deviceID {
			return true
		}
	}

	return false
}

// AddPair records a pair; false when it already exists.
func (st *State) AddPair(deviceID, agentID string, now time.Time) bool {
	if st.IsPaired(deviceID, agentID) {
		return false
	}
	st.Pairs = append(st.Pairs, Pair{DeviceID: deviceID, AgentID: agentID, PairedAt: now})

	return true
}

// RemovePair forgets a pair; false when there was none.
func (st *State) RemovePair(deviceID, agentID string) bool {
	for i, p := range st.Pairs {
		if p.DeviceID == deviceID && p.AgentID == agentID {
			st.Pairs = append(st.Pairs[:i], st.Pairs[i+1:]...)

			return true
		}
	}

	return false
}

// AgentByToken finds the agent presenting a bearer token. Every stored token
// is compared in constant time; a home hub has a handful of agents.
func (st *State) AgentByToken(token string) *Agent {
	var found *Agent
	for _, a := range st.Agents {
		if len(a.Token) == len(token) && subtle.ConstantTimeCompare([]byte(a.Token), []byte(token)) == 1 {
			found = a
		}
	}

	return found
}

// Store is the state file behind a mutex.
type Store struct {
	path string

	mu    sync.Mutex
	state State
}

// Open loads the state file; a missing file is an empty state.
func Open(path string) (*Store, error) {
	s := &Store{path: path, state: newState()}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if s.state.Devices == nil {
			s.state.Devices = map[string]*Device{}
		}
		if s.state.Agents == nil {
			s.state.Agents = map[string]*Agent{}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}

	return s, nil
}

// View runs fn with the state locked for reading.
func (s *Store) View(fn func(st *State)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn(&s.state)
}

// Update runs fn with the state locked and saves the file when fn returns
// nil. An error from fn is returned as is and nothing is written, so callers
// decide (validate, authenticate) before they mutate.
func (s *Store) Update(fn func(st *State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := fn(&s.state); err != nil {
		return err
	}

	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	return atomicfile.WriteJSON(s.path, &s.state, stateFileMode)
}

// Purge drops devices and agents that have no pairs and have not been heard
// from for maxAge; it returns how many went and saves only when some did.
func (s *Store) Purge(now time.Time, maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for id, d := range s.state.Devices {
		if !s.state.HasPairs(id) && now.Sub(d.LastSeen) > maxAge {
			delete(s.state.Devices, id)
			removed++
		}
	}
	for id, a := range s.state.Agents {
		if !s.state.agentHasPairs(id) && now.Sub(a.LastSeen) > maxAge {
			delete(s.state.Agents, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}

	return removed, s.saveLocked()
}

func (st *State) agentHasPairs(agentID string) bool {
	for _, p := range st.Pairs {
		if p.AgentID == agentID {
			return true
		}
	}

	return false
}
