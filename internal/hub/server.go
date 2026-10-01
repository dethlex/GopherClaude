package hub

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Policy constants, exported so the tests and the clients share them.
const (
	OnlineWindow = 3 * time.Minute  // a display heartbeats every 10 s unpaired, every minute linked
	HintWindow   = 2 * time.Minute  // "nearby" means seen this recently behind the same public IP
	CodeTTL      = 10 * time.Minute // a code is matchable this long after the heartbeat that carried it
	PairLimit    = 5
	PairWindow   = time.Minute
	MaxBody      = 16 << 10
	MaxNameLen   = 32

	maxAgentAddrs = 8
	maxPort       = 65535
)

// DeviceHeartbeat is what a display posts. Secret is set on first contact
// only; Sig signs device_id+nonce afterwards; Code is shown while unpaired.
type DeviceHeartbeat struct {
	DeviceID   string `json:"device_id"`
	Secret     string `json:"secret,omitempty"`
	Sig        string `json:"sig,omitempty"`
	Addr       string `json:"addr,omitempty"`
	Code       string `json:"code,omitempty"`
	FW         string `json:"fw,omitempty"`
	UptimeS    int64  `json:"uptime_s"`
	HeapFree   int    `json:"heap_free"`
	BatteryMV  int    `json:"battery_mv"`
	RSSI       int    `json:"rssi"`
	Reconnects int    `json:"reconnects"`
	LastError  string `json:"last_error,omitempty"`
}

// AgentAddr tells a display where one of its agents listens.
type AgentAddr struct {
	AgentID string   `json:"agent_id"`
	Addrs   []string `json:"addrs"`
	Port    int      `json:"port"`
}

type DeviceReply struct {
	Nonce  string      `json:"nonce"`
	Name   string      `json:"name,omitempty"`
	Agents []AgentAddr `json:"agents"`
}

type AgentHeartbeat struct {
	AgentID string   `json:"agent_id"`
	Addrs   []string `json:"addrs"`
	Port    int      `json:"port"`
	Version string   `json:"version,omitempty"`
}

type DeviceView struct {
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name,omitempty"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
	Metrics  Metrics   `json:"metrics"`
}

// Hint points the agent at an unpaired display behind the same public IP.
type Hint struct {
	DeviceID string    `json:"device_id"`
	SeenAt   time.Time `json:"seen_at"`
}

type AgentReply struct {
	Devices []DeviceView `json:"devices"`
	Hint    *Hint        `json:"hint"`
}

type PairRequest struct {
	Code string `json:"code"`
}

type PairReply struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
}

type NameRequest struct {
	Name string `json:"name"`
}

type errorReply struct {
	Error string `json:"error"`
}

// httpError carries the status a handler decided on out of a store Update.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, msg string) error { return &httpError{status: status, msg: msg} }

var (
	errUnregistered = fail(http.StatusUnauthorized, "unregistered device")
	errBadAgent     = fail(http.StatusUnauthorized, "unknown agent or bad token")
	errNoCode       = fail(http.StatusNotFound, "no device shows this code")
	errAmbiguous    = fail(http.StatusConflict, "several devices show this code")
	errNotPaired    = fail(http.StatusNotFound, "not paired")
	errForbidden    = fail(http.StatusForbidden, "device is not paired with this agent")
)

// challenge is the 401 that carries a fresh nonce for a known device.
type challenge struct {
	nonce string
}

func (c *challenge) Error() string { return "signature required" }

// Server serves the Hub API over a Store.
type Server struct {
	store   *Store
	logger  *slog.Logger
	limiter *Limiter
	now     func() time.Time
	mux     *http.ServeMux
}

func NewServer(store *Store, logger *slog.Logger) *Server {
	s := &Server{
		store:   store,
		logger:  logger.With("module", "hub"),
		limiter: NewLimiter(PairLimit, PairWindow),
		now:     time.Now,
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("POST /v1/device/heartbeat", s.deviceHeartbeat)
	s.mux.HandleFunc("POST /v1/agent/heartbeat", s.agentHeartbeat)
	s.mux.HandleFunc("POST /v1/pair", s.pair)
	s.mux.HandleFunc("DELETE /v1/pair/{device_id}", s.unpair)
	s.mux.HandleFunc("PUT /v1/devices/{device_id}/name", s.rename)

	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

// decode reads a bounded JSON body; a body over MaxBody is 413, anything
// unparsable 400.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fail(http.StatusRequestEntityTooLarge, "body too large")
		}

		return fail(http.StatusBadRequest, "malformed JSON")
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

// writeError maps the handler's error to a response: a challenge carries its
// nonce, an httpError its status, anything else is a 500 that gets logged.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	var ch *challenge
	if errors.As(err, &ch) {
		writeJSON(w, http.StatusUnauthorized, DeviceReply{Nonce: ch.nonce, Agents: []AgentAddr{}})

		return
	}
	var he *httpError
	if errors.As(err, &he) {
		writeJSON(w, he.status, errorReply{Error: he.msg})

		return
	}
	s.logger.Error("request failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, errorReply{Error: "internal error"})
}

func (s *Server) deviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb DeviceHeartbeat
	if err := decode(w, r, &hb); err != nil {
		s.writeError(w, err)

		return
	}
	if !IsHex(hb.DeviceID, IDLen) || (hb.Code != "" && !IsCode(hb.Code)) || (hb.Secret != "" && !IsHex(hb.Secret, SecretLen)) {
		s.writeError(w, fail(http.StatusBadRequest, "malformed device_id, secret or code"))

		return
	}

	now := s.now()
	ip := PublicIP(r)
	var reply DeviceReply
	err := s.store.Update(func(st *State) error {
		dev := st.Devices[hb.DeviceID]
		switch {
		case dev == nil && hb.Secret == "":
			return errUnregistered
		case dev == nil:
			// Trust on first use: the secret arrives once, in the clear.
			dev = &Device{ID: hb.DeviceID, Secret: hb.Secret, RegisteredAt: now}
			st.Devices[dev.ID] = dev
			s.logger.Info("device registered", "device", dev.ID, "public_ip", ip)
		case hb.Sig == "" || dev.Nonce == "" || !Equal(hb.Sig, DeviceSignature(dev.Secret, dev.ID, dev.Nonce)):
			// Known device, no valid signature: issue a challenge and record
			// nothing else — this is also what a stranger gets.
			dev.Nonce = NewNonce()

			return &challenge{nonce: dev.Nonce}
		}

		dev.Nonce = NewNonce()
		dev.LastSeen = now
		dev.PublicIP = ip
		dev.Addr = hb.Addr
		dev.Metrics = Metrics{FW: hb.FW, UptimeS: hb.UptimeS, HeapFree: hb.HeapFree, BatteryMV: hb.BatteryMV, RSSI: hb.RSSI, Reconnects: hb.Reconnects, LastError: hb.LastError}
		if st.HasPairs(dev.ID) || hb.Code == "" {
			dev.Code, dev.CodeAt = "", time.Time{}
		} else {
			dev.Code, dev.CodeAt = hb.Code, now
		}

		reply = DeviceReply{Nonce: dev.Nonce, Name: dev.Name, Agents: []AgentAddr{}}
		for _, a := range st.PairedAgents(dev.ID) {
			reply.Agents = append(reply.Agents, AgentAddr{AgentID: a.ID, Addrs: a.Addrs, Port: a.Port})
		}

		return nil
	})
	if err != nil {
		// A challenge changed the nonce: persist it so a restart in between
		// does not strand the device, then answer.
		var ch *challenge
		if errors.As(err, &ch) {
			s.store.Update(func(*State) error { return nil })
		}
		s.writeError(w, err)

		return
	}
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	token := BearerToken(r)
	if token == "" {
		s.writeError(w, errBadAgent)

		return
	}
	var hb AgentHeartbeat
	if err := decode(w, r, &hb); err != nil {
		s.writeError(w, err)

		return
	}
	if !IsHex(hb.AgentID, IDLen) || hb.Port < 1 || hb.Port > maxPort || len(hb.Addrs) > maxAgentAddrs {
		s.writeError(w, fail(http.StatusBadRequest, "malformed agent_id, port or addrs"))

		return
	}

	now := s.now()
	ip := PublicIP(r)
	var reply AgentReply
	err := s.store.Update(func(st *State) error {
		ag := st.Agents[hb.AgentID]
		switch {
		case ag == nil:
			ag = &Agent{ID: hb.AgentID, Token: token, RegisteredAt: now}
			st.Agents[ag.ID] = ag
			s.logger.Info("agent registered", "agent", ag.ID, "public_ip", ip)
		case !Equal(ag.Token, token):
			return errBadAgent
		}
		ag.Addrs, ag.Port, ag.Version, ag.PublicIP, ag.LastSeen = hb.Addrs, hb.Port, hb.Version, ip, now
		if ag.Addrs == nil {
			ag.Addrs = []string{}
		}

		reply = AgentReply{Devices: []DeviceView{}}
		for _, d := range st.PairedDevices(ag.ID) {
			reply.Devices = append(reply.Devices, DeviceView{
				DeviceID: d.ID, Name: d.Name, LastSeen: d.LastSeen, Metrics: d.Metrics,
				Online: now.Sub(d.LastSeen) <= OnlineWindow,
			})
		}
		reply.Hint = nearbyUnpaired(st, ip, now)

		return nil
	})
	if err != nil {
		s.writeError(w, err)

		return
	}
	writeJSON(w, http.StatusOK, reply)
}

// nearbyUnpaired picks the most recently seen unpaired device behind the
// agent's public IP that is currently showing a code.
func nearbyUnpaired(st *State, ip string, now time.Time) *Hint {
	var best *Device
	for _, d := range st.Devices {
		if ip == "" || d.PublicIP != ip || d.Code == "" || st.HasPairs(d.ID) || now.Sub(d.LastSeen) > HintWindow {
			continue
		}
		if best == nil || d.LastSeen.After(best.LastSeen) {
			best = d
		}
	}
	if best == nil {
		return nil
	}

	return &Hint{DeviceID: best.ID, SeenAt: best.LastSeen}
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	token := BearerToken(r)
	if token == "" {
		s.writeError(w, errBadAgent)

		return
	}
	var req PairRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, err)

		return
	}
	if !IsCode(req.Code) {
		s.writeError(w, fail(http.StatusBadRequest, "a code is six digits"))

		return
	}

	now := s.now()
	ip := PublicIP(r)
	var reply PairReply
	err := s.store.Update(func(st *State) error {
		ag := st.AgentByToken(token)
		if ag == nil {
			return errBadAgent
		}
		if !s.limiter.Allow(ag.ID, now) {
			return fail(http.StatusTooManyRequests, "too many pairing attempts, wait a minute")
		}

		var candidates []*Device
		for _, d := range st.Devices {
			if d.Code == req.Code && now.Sub(d.CodeAt) <= CodeTTL && !st.HasPairs(d.ID) {
				candidates = append(candidates, d)
			}
		}
		if len(candidates) > 1 {
			// Two displays happen to show the same code: the one behind the
			// agent's own public IP is the one on its desk.
			var local []*Device
			for _, d := range candidates {
				if ip != "" && d.PublicIP == ip {
					local = append(local, d)
				}
			}
			candidates = local
		}
		switch len(candidates) {
		case 0:
			return errNoCode
		case 1:
		default:
			return errAmbiguous
		}

		dev := candidates[0]
		st.AddPair(dev.ID, ag.ID, now)
		dev.Code, dev.CodeAt = "", time.Time{}
		reply = PairReply{DeviceID: dev.ID, DeviceToken: DeviceToken(dev.Secret, ag.ID)}
		s.logger.Info("paired", "device", dev.ID, "agent", ag.ID)

		return nil
	})
	if err != nil {
		s.writeError(w, err)

		return
	}
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) unpair(w http.ResponseWriter, r *http.Request) {
	token := BearerToken(r)
	deviceID := r.PathValue("device_id")
	if token == "" {
		s.writeError(w, errBadAgent)

		return
	}
	if !IsHex(deviceID, IDLen) {
		s.writeError(w, fail(http.StatusBadRequest, "malformed device_id"))

		return
	}
	err := s.store.Update(func(st *State) error {
		ag := st.AgentByToken(token)
		if ag == nil {
			return errBadAgent
		}
		if !st.RemovePair(deviceID, ag.ID) {
			return errNotPaired
		}
		s.logger.Info("unpaired", "device", deviceID, "agent", ag.ID)

		return nil
	})
	if err != nil {
		s.writeError(w, err)

		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	token := BearerToken(r)
	deviceID := r.PathValue("device_id")
	if token == "" {
		s.writeError(w, errBadAgent)

		return
	}
	var req NameRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, err)

		return
	}
	name := strings.TrimSpace(req.Name)
	if !IsHex(deviceID, IDLen) || !validName(name) {
		s.writeError(w, fail(http.StatusBadRequest, "malformed device_id or name"))

		return
	}
	err := s.store.Update(func(st *State) error {
		ag := st.AgentByToken(token)
		if ag == nil {
			return errBadAgent
		}
		dev := st.Devices[deviceID]
		if dev == nil || !st.IsPaired(deviceID, ag.ID) {
			return errForbidden
		}
		dev.Name = name

		return nil
	})
	if err != nil {
		s.writeError(w, err)

		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validName accepts a short, printable, non-empty UTF-8 name.
func validName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameLen {
		return false
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return false
		}
	}

	return true
}
