package hub

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/infra/display"
)

type bench struct {
	t      *testing.T
	store  *Store
	srv    *Server
	now    time.Time
	path   string
	logger *slog.Logger
}

func newBench(t *testing.T) *bench {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b := &bench{t: t, store: store, now: t0, path: path, logger: slog.Default()}
	b.srv = NewServer(store, b.logger)
	b.srv.now = func() time.Time { return b.now }

	return b
}

// restart reopens the state file into a new server, as after a redeploy.
func (b *bench) restart() {
	store, err := Open(b.path)
	if err != nil {
		b.t.Fatal(err)
	}
	b.store = store
	b.srv = NewServer(store, b.logger)
	b.srv.now = func() time.Time { return b.now }
}

type call struct {
	method, path, ip, bearer string
	body                     any
	raw                      []byte
}

func (b *bench) do(c call) (int, map[string]any) {
	b.t.Helper()
	var body []byte
	if c.raw != nil {
		body = c.raw
	} else if c.body != nil {
		body, _ = json.Marshal(c.body)
	}
	r := httptest.NewRequest(c.method, c.path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if c.ip != "" {
		r.Header.Set("X-Forwarded-For", c.ip)
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	w := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(w, r)
	var out map[string]any
	if w.Body.Len() > 0 {
		json.Unmarshal(w.Body.Bytes(), &out)
	}

	return w.Code, out
}

func (b *bench) deviceBeat(id, ip string, fields map[string]any) (int, map[string]any) {
	body := map[string]any{"device_id": id}
	for k, v := range fields {
		body[k] = v
	}

	return b.do(call{method: http.MethodPost, path: "/v1/device/heartbeat", ip: ip, body: body})
}

// signedBeat performs the challenge dance: an unsigned heartbeat to get a
// nonce, then the signed one. It returns the signed reply.
func (b *bench) signedBeat(id, secret, ip string, fields map[string]any) (int, map[string]any) {
	b.t.Helper()
	code, rep := b.deviceBeat(id, ip, fields)
	if code != http.StatusUnauthorized || rep["nonce"] == nil {
		b.t.Fatalf("challenge: %d %v", code, rep)
	}
	f := map[string]any{"sig": DeviceSignature(secret, id, rep["nonce"].(string))}
	for k, v := range fields {
		f[k] = v
	}

	return b.deviceBeat(id, ip, f)
}

func (b *bench) agentBeat(id, token, ip string, extra map[string]any) (int, map[string]any) {
	body := map[string]any{"agent_id": id, "addrs": []string{"192.168.31.190"}, "port": 7070, "version": "test"}
	for k, v := range extra {
		body[k] = v
	}

	return b.do(call{method: http.MethodPost, path: "/v1/agent/heartbeat", ip: ip, bearer: token, body: body})
}

func (b *bench) pair(token, ip, code string) (int, map[string]any) {
	return b.do(call{method: http.MethodPost, path: "/v1/pair", ip: ip, bearer: token, body: map[string]string{"code": code}})
}

const (
	ipHome   = "203.0.113.5"
	ipOffice = "198.51.100.9"
)

func TestHealthz(t *testing.T) {
	b := newBench(t)
	code, _ := b.do(call{method: http.MethodGet, path: "/healthz"})
	if code != http.StatusOK {
		t.Fatal(code)
	}
}

func TestDeviceRegistersThenMustSign(t *testing.T) {
	b := newBench(t)
	code, rep := b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "fw": "570237b", "heap_free": 27600, "code": "123456"})
	if code != http.StatusOK || !IsHex(rep["nonce"].(string), NonceLen) || rep["agents"] == nil {
		t.Fatalf("first contact: %d %v", code, rep)
	}
	nonce := rep["nonce"].(string)

	// Unsigned again: a challenge, not an acceptance, and nothing recorded.
	code, rep = b.deviceBeat(devA, ipHome, map[string]any{"heap_free": 1})
	if code != http.StatusUnauthorized || rep["nonce"] == nil || rep["nonce"] == nonce {
		t.Fatalf("challenge: %d %v", code, rep)
	}
	b.store.View(func(st *State) {
		if st.Devices[devA].Metrics.HeapFree != 27600 {
			t.Fatal("a challenged heartbeat must not update metrics")
		}
	})
	nonce = rep["nonce"].(string)

	// Signed with the challenge nonce: accepted, new nonce issued.
	code, rep = b.deviceBeat(devA, ipHome, map[string]any{"sig": DeviceSignature(secA, devA, nonce), "heap_free": 2})
	if code != http.StatusOK || rep["nonce"] == nonce {
		t.Fatalf("signed: %d %v", code, rep)
	}
	// Replay of the same signature: rejected.
	code, _ = b.deviceBeat(devA, ipHome, map[string]any{"sig": DeviceSignature(secA, devA, nonce)})
	if code != http.StatusUnauthorized {
		t.Fatalf("replay accepted: %d", code)
	}
	// Wrong secret: rejected.
	code, rep = b.deviceBeat(devA, ipHome, nil)
	code, _ = b.deviceBeat(devA, ipHome, map[string]any{"sig": DeviceSignature(secB, devA, rep["nonce"].(string))})
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong secret accepted: %d", code)
	}
	// Unknown device without a secret: nothing to register.
	code, _ = b.deviceBeat(devB, ipHome, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unknown device without secret: %d", code)
	}
}

func TestDeviceChallengeAfterReboot(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA})
	// The display forgot the nonce (it rebooted) but still has its secret.
	code, rep := b.signedBeat(devA, secA, ipHome, map[string]any{"heap_free": 5})
	if code != http.StatusOK || rep["nonce"] == nil {
		t.Fatalf("%d %v", code, rep)
	}
}

func TestDeviceSecretCannotBeReplaced(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA})
	code, _ := b.deviceBeat(devA, ipOffice, map[string]any{"secret": secB})
	if code != http.StatusUnauthorized {
		t.Fatalf("re-registration accepted: %d", code)
	}
	if code, _ := b.signedBeat(devA, secA, ipHome, nil); code != http.StatusOK {
		t.Fatal("the original secret must still work")
	}
	if code, _ := b.signedBeat(devA, secB, ipHome, nil); code != http.StatusUnauthorized {
		t.Fatal("the attacker's secret must not")
	}
}

func TestDeviceBadInput(t *testing.T) {
	b := newBench(t)
	if code, _ := b.deviceBeat("short", ipHome, map[string]any{"secret": secA}); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	if code, _ := b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "12ab"}); code != http.StatusBadRequest {
		t.Fatalf("bad code: %d", code)
	}
	if code, _ := b.do(call{method: http.MethodPost, path: "/v1/device/heartbeat", raw: []byte("{not json")}); code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", code)
	}
}

func TestBodyLimit(t *testing.T) {
	b := newBench(t)
	raw := []byte(`{"device_id":"` + devA + `","secret":"` + secA + `","last_error":"` + strings.Repeat("x", MaxBody) + `"}`)
	code, _ := b.do(call{method: http.MethodPost, path: "/v1/device/heartbeat", raw: raw})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", code)
	}
}

func TestAgentRegistersAndKeepsToken(t *testing.T) {
	b := newBench(t)
	if code, _ := b.do(call{method: http.MethodPost, path: "/v1/agent/heartbeat", body: map[string]any{"agent_id": agX}}); code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", code)
	}
	code, rep := b.agentBeat(agX, tokX, ipHome, nil)
	if code != http.StatusOK || rep["devices"] == nil || rep["hint"] != nil {
		t.Fatalf("first contact: %d %v", code, rep)
	}
	if code, _ := b.agentBeat(agX, tokY, ipHome, nil); code != http.StatusUnauthorized {
		t.Fatalf("other token for a known agent accepted: %d", code)
	}
	if code, _ := b.agentBeat(agX, tokX, ipHome, map[string]any{"port": 70000}); code != http.StatusBadRequest {
		t.Fatalf("bad port: %d", code)
	}
	b.store.View(func(st *State) {
		a := st.Agents[agX]
		if a == nil || a.PublicIP != ipHome || a.Port != 7070 || a.Addrs[0] != "192.168.31.190" {
			t.Fatalf("agent %+v", a)
		}
	})
}

func TestHintSamePublicIP(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456"})
	_, rep := b.agentBeat(agX, tokX, ipOffice, nil)
	if rep["hint"] != nil {
		t.Fatal("different public IP must not hint")
	}
	_, rep = b.agentBeat(agX, tokX, ipHome, nil)
	hint, _ := rep["hint"].(map[string]any)
	if hint == nil || hint["device_id"] != devA {
		t.Fatalf("hint %v", rep["hint"])
	}
	// Too old to be "nearby".
	b.now = b.now.Add(HintWindow + time.Second)
	if _, rep = b.agentBeat(agX, tokX, ipHome, nil); rep["hint"] != nil {
		t.Fatal("stale device must not hint")
	}
}

func TestPairThenUnpairLifecycle(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456", "fw": "570237b"})
	b.agentBeat(agX, tokX, ipHome, nil)

	if code, _ := b.pair(tokX, ipHome, "654321"); code != http.StatusNotFound {
		t.Fatalf("wrong code: %d", code)
	}
	if code, _ := b.pair(tokX, ipHome, "12"); code != http.StatusBadRequest {
		t.Fatalf("malformed code: %d", code)
	}
	if code, _ := b.pair(tokY, ipHome, "123456"); code != http.StatusUnauthorized {
		t.Fatalf("unknown agent: %d", code)
	}
	code, rep := b.pair(tokX, ipHome, "123456")
	if code != http.StatusOK || rep["device_id"] != devA || rep["device_token"] != display.TokenFor(secA, agX) {
		t.Fatalf("pair: %d %v", code, rep)
	}
	if code, _ := b.pair(tokX, ipHome, "123456"); code != http.StatusNotFound {
		t.Fatal("a used code must not pair again")
	}
	if _, rep := b.agentBeat(agX, tokX, ipHome, nil); rep["hint"] != nil {
		t.Fatal("a paired device is no hint")
	}

	// The display's next heartbeat brings the agent. Metrics travel with every
	// heartbeat and replace the stored set whole.
	_, rep = b.signedBeat(devA, secA, ipHome, map[string]any{"code": "999999", "fw": "570237b"})
	agents, _ := rep["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("agents %v", rep["agents"])
	}
	ag := agents[0].(map[string]any)
	if ag["agent_id"] != agX || ag["port"].(float64) != 7070 || ag["addrs"].([]any)[0] != "192.168.31.190" {
		t.Fatalf("agent entry %v", ag)
	}
	b.store.View(func(st *State) {
		if st.Devices[devA].Code != "" {
			t.Fatal("a paired device must not keep a code")
		}
	})

	// The agent sees the device with metrics and online.
	_, rep = b.agentBeat(agX, tokX, ipHome, nil)
	devs, _ := rep["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("devices %v", rep["devices"])
	}
	dv := devs[0].(map[string]any)
	if dv["device_id"] != devA || dv["online"] != true || dv["metrics"].(map[string]any)["fw"] != "570237b" {
		t.Fatalf("device view %v", dv)
	}
	b.now = b.now.Add(OnlineWindow + time.Second)
	_, rep = b.agentBeat(agX, tokX, ipHome, nil)
	if rep["devices"].([]any)[0].(map[string]any)["online"] != false {
		t.Fatal("device must go offline after the window")
	}

	// Rename, by the paired agent only.
	if code, _ := b.do(call{method: http.MethodPut, path: "/v1/devices/" + devA + "/name", bearer: tokX, body: map[string]string{"name": "Kitchen"}}); code != http.StatusNoContent {
		t.Fatalf("rename: %d", code)
	}
	b.agentBeat(agY, tokY, ipOffice, nil)
	if code, _ := b.do(call{method: http.MethodPut, path: "/v1/devices/" + devA + "/name", bearer: tokY, body: map[string]string{"name": "Mine"}}); code != http.StatusForbidden {
		t.Fatalf("rename by a stranger: %d", code)
	}
	if code, _ := b.do(call{method: http.MethodPut, path: "/v1/devices/" + devA + "/name", bearer: tokX, body: map[string]string{"name": strings.Repeat("n", MaxNameLen+1)}}); code != http.StatusBadRequest {
		t.Fatalf("long name: %d", code)
	}
	_, rep = b.signedBeat(devA, secA, ipHome, nil)
	if rep["name"] != "Kitchen" {
		t.Fatalf("name in device reply: %v", rep["name"])
	}

	// Unpair: the display gets no agents and may pair again with a new code.
	if code, _ := b.do(call{method: http.MethodDelete, path: "/v1/pair/" + devA, bearer: tokX}); code != http.StatusNoContent {
		t.Fatalf("unpair: %d", code)
	}
	if code, _ := b.do(call{method: http.MethodDelete, path: "/v1/pair/" + devA, bearer: tokX}); code != http.StatusNotFound {
		t.Fatalf("unpair twice: %d", code)
	}
	_, rep = b.signedBeat(devA, secA, ipHome, map[string]any{"code": "222222"})
	if len(rep["agents"].([]any)) != 0 {
		t.Fatalf("agents after unpair %v", rep["agents"])
	}
	if code, _ := b.pair(tokX, ipHome, "222222"); code != http.StatusOK {
		t.Fatalf("pair again: %d", code)
	}
}

func TestPairCodeExpires(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456"})
	b.agentBeat(agX, tokX, ipHome, nil)
	b.now = b.now.Add(CodeTTL + time.Second)
	if code, _ := b.pair(tokX, ipHome, "123456"); code != http.StatusNotFound {
		t.Fatalf("expired code paired: %d", code)
	}
}

func TestPairAmbiguousCode(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456"})
	b.deviceBeat(devB, ipOffice, map[string]any{"secret": secB, "code": "123456"})
	b.agentBeat(agX, tokX, ipHome, nil)
	code, rep := b.pair(tokX, ipHome, "123456")
	if code != http.StatusOK || rep["device_id"] != devA {
		t.Fatalf("the device behind the agent's IP must win: %d %v", code, rep)
	}

	c := newBench(t)
	c.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456"})
	c.deviceBeat(devB, ipHome, map[string]any{"secret": secB, "code": "123456"})
	c.agentBeat(agX, tokX, ipHome, nil)
	if code, _ := c.pair(tokX, ipHome, "123456"); code != http.StatusConflict {
		t.Fatalf("two candidates behind the same IP: %d", code)
	}
}

func TestPairRateLimit(t *testing.T) {
	b := newBench(t)
	b.agentBeat(agX, tokX, ipHome, nil)
	for i := 0; i < PairLimit; i++ {
		if code, _ := b.pair(tokX, ipHome, "000000"); code != http.StatusNotFound {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, _ := b.pair(tokX, ipHome, "000000"); code != http.StatusTooManyRequests {
		t.Fatal("sixth attempt within a minute must be refused")
	}
	b.now = b.now.Add(PairWindow + time.Second)
	if code, _ := b.pair(tokX, ipHome, "000000"); code != http.StatusNotFound {
		t.Fatal("budget must return after the window")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	b := newBench(t)
	b.deviceBeat(devA, ipHome, map[string]any{"secret": secA, "code": "123456"})
	b.agentBeat(agX, tokX, ipHome, nil)
	b.pair(tokX, ipHome, "123456")

	b.restart()
	if code, _ := b.agentBeat(agX, tokX, ipHome, nil); code != http.StatusOK {
		t.Fatal("agent token lost")
	}
	_, rep := b.signedBeat(devA, secA, ipHome, nil)
	if len(rep["agents"].([]any)) != 1 {
		t.Fatalf("pair lost: %v", rep)
	}
}
