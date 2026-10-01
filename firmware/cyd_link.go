//go:build esp32

package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"device/esp"
	"encoding/hex"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/drivers/netdev"

	"claudecontrol/firmware/internal/hello"
)

// Development overrides until the Hub exists (stage A4): where the agent
// is and who it is, baked in with -ldflags -X. With both set, they win over
// the record's cache so a test bench never chases a stale address.
var (
	devAgentAddr string
	devAgentID   string
)

const (
	linkDialTimeout  = 5 * time.Second
	linkHelloTimeout = 5 * time.Second
	linkIdleTimeout  = 10 * time.Second // the agent sends every 2 s
	linkRetryPause   = 5 * time.Second
	linkWriteTimeout = time.Second
	linkQueueDepth   = 2    // frames waiting for the core; older ones are stale
	linkReadBuf      = 8192 // a frame is ~3.5 KB
	linkDNSRetries   = 3
	agentAddrsStored = 3
	rngWordBytes     = 4
)

var (
	linkMu    sync.Mutex
	linkConn  net.Conn // nil while down
	linkLines = hello.NewLineQueue(linkQueueDepth)

	// tcpFrame remembers which link delivered the frame the core is about
	// to answer, so the echo goes back the same way.
	tcpFrame bool

	// pendingAgent is the address that just completed a handshake; the main
	// goroutine stores it (flash writes stay on the goroutine that has
	// always done them), see pollLine.
	pendingAgent struct {
		addr, id string
	}
)

// ensureIdentity gives the display its random id and secret on first use.
// The hardware RNG is only truly random while the radio is on, which is why
// this runs after WiFi is up and not at calibration time.
func ensureIdentity() {
	rec := settingsRecord
	if rec.HasIdentity {
		return
	}
	fillRandom(rec.DeviceID[:])
	fillRandom(rec.Secret[:])
	rec.HasIdentity = true
	writeStore(rec)
}

func fillRandom(b []byte) {
	for i := 0; i < len(b); i += rngWordBytes {
		w := esp.RNG.DATA.Get()
		for j := 0; j < rngWordBytes && i+j < len(b); j++ {
			b[i+j] = byte(w >> (8 * j))
		}
	}
}

// deviceToken is HMAC-SHA256(secret, agent-id hex), as the Hub derives it.
func deviceToken(agentID string) string {
	mac := hmac.New(sha256.New, settingsRecord.Secret[:])
	mac.Write([]byte(agentID))

	return hex.EncodeToString(mac.Sum(nil))
}

// agentTargets lists where to dial, in order: the development override,
// else the record's cached addresses.
func agentTargets() (addrs []string, agentID string) {
	if devAgentAddr != "" && devAgentID != "" {
		return []string{devAgentAddr}, devAgentID
	}
	rec := settingsRecord
	if !rec.Paired || rec.AgentPort == 0 {
		return nil, ""
	}
	for _, a := range rec.AgentAddrs {
		if a == [4]byte{} {
			continue
		}
		addrs = append(addrs, netip.AddrFrom4(a).String()+":"+strconv.Itoa(int(rec.AgentPort)))
	}

	return addrs, hex.EncodeToString(rec.AgentID[:])
}

// rememberAgent caches the address that worked so the next boot can dial it
// without the Hub.
func rememberAgent(addr, agentID string) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().Is4() {
		return
	}
	id, err := hex.DecodeString(agentID)
	if err != nil || len(id) != len(settingsRecord.AgentID) {
		return
	}

	rec := settingsRecord
	first := ap.Addr().As4()
	if rec.Paired && rec.AgentAddrs[0] == first && rec.AgentPort == ap.Port() && rec.AgentID == [16]byte(id) {
		return // nothing changed, spare the flash
	}
	// Keep the other cached addresses behind the one that worked.
	var addrs [agentAddrsStored][4]byte
	addrs[0] = first
	n := 1
	for _, a := range rec.AgentAddrs {
		if n < agentAddrsStored && a != first && a != [4]byte{} {
			addrs[n] = a
			n++
		}
	}
	rec.AgentAddrs = addrs
	rec.AgentPort = ap.Port()
	copy(rec.AgentID[:], id)
	rec.Paired = true
	writeStore(rec)
}

// startLink makes the IP stack available to the net package and starts the
// link goroutine. Called once, after the radio is up.
func startLink() {
	ensureIdentity()
	netdev.UseNetdev(&radioNetdev{})
	go linkLoop()
}

// linkLoop keeps one connection to the agent alive: dial, HELLO, WELCOME,
// then read frames until the connection dies, then try again.
func linkLoop() {
	for {
		addrs, agentID := agentTargets()
		if len(addrs) == 0 {
			time.Sleep(linkRetryPause)

			continue
		}
		for _, addr := range addrs {
			serveLink(addr, agentID)
		}
		time.Sleep(linkRetryPause)
	}
}

// serveLink runs one connection: dial, HELLO, WELCOME, then frames until
// the connection dies. It returns once the connection is gone.
func serveLink(addr, agentID string) {
	conn, err := net.DialTimeout("tcp", addr, linkDialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(linkHelloTimeout))
	id := hex.EncodeToString(settingsRecord.DeviceID[:])
	if _, err := conn.Write([]byte(hello.Line(id, fwVersion, deviceToken(agentID)))); err != nil {
		return
	}
	r := bufio.NewReaderSize(conn, linkReadBuf)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	if got, ok := hello.ParseWelcome(line); !ok || got != agentID {
		// Not our agent (or not an agent at all): never trust frames from it.
		return
	}

	linkMu.Lock()
	linkConn = conn
	pendingAgent.addr, pendingAgent.id = addr, agentID
	linkMu.Unlock()

	for {
		conn.SetReadDeadline(time.Now().Add(linkIdleTimeout))
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		linkMu.Lock()
		linkLines.Push(strings.TrimRight(line, "\r\n"))
		linkMu.Unlock()
	}

	linkMu.Lock()
	linkConn = nil
	linkMu.Unlock()
}

// pollLine hands the core the next frame: TCP first, the USB serial port as
// the fallback and the development path.
func pollLine() (string, bool) {
	linkMu.Lock()
	line, ok := linkLines.Pop()
	remember := pendingAgent
	pendingAgent.addr = ""
	linkMu.Unlock()
	if remember.addr != "" {
		rememberAgent(remember.addr, remember.id)
	}
	if ok {
		tcpFrame = true

		return line, true
	}

	line, ok = pollSerialLine()
	if ok {
		tcpFrame = false
	}

	return line, ok
}

// sendCommand goes to whoever is connected over TCP, else to the serial port.
func sendCommand(cmd string) {
	sendLinkLine("CMD " + cmd)
}

// sendEcho answers the frame over the link it came from.
func sendEcho(text string) {
	if tcpFrame {
		writeTCP(text)

		return
	}
	sendSerialLine(text)
}

func sendLinkLine(line string) {
	if !writeTCP(line) {
		sendSerialLine(line)
	}
}

// writeTCP writes a line to the agent connection; false when there is none
// or the write failed (the reader notices the dead connection on its own).
func writeTCP(line string) bool {
	linkMu.Lock()
	conn := linkConn
	linkMu.Unlock()
	if conn == nil {
		return false
	}
	conn.SetWriteDeadline(time.Now().Add(linkWriteTimeout))
	_, err := conn.Write([]byte(line + "\n"))

	return err == nil
}

// radioNetdev adapts lneto's Berkeley layer to TinyGo's netdev interface so
// the standard net package can dial over the radio. It is the spike's
// adapter minus everything we do not use.
type radioNetdev struct{}

func (radioNetdev) GetHostByName(name string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(name); err == nil {
		return addr, nil
	}
	addrs, err := radioStack.LnetoStack().StackRetrying(radioBackoff).DoLookupIP(name, linkDialTimeout, linkDNSRetries)
	if err != nil {
		return netip.Addr{}, err
	}

	return addrs[0], nil
}

func (radioNetdev) Addr() (netip.Addr, error) {
	return radioIP, nil
}

func (radioNetdev) Socket(domain, stype, protocol int) (int, error) {
	return radioBerkeley.Socket(domain, stype, protocol)
}

func (radioNetdev) Bind(fd int, ip netip.AddrPort) error { return radioBerkeley.Bind(fd, ip) }

func (d radioNetdev) Connect(fd int, host string, ip netip.AddrPort) error {
	if (!ip.Addr().IsValid() || ip.Addr().IsUnspecified()) && host != "" {
		resolved, err := d.GetHostByName(host)
		if err != nil {
			return err
		}
		ip = netip.AddrPortFrom(resolved, ip.Port())
	}

	return radioBerkeley.Connect(fd, host, ip)
}

func (radioNetdev) Listen(fd, backlog int) error { return radioBerkeley.Listen(fd, backlog) }

func (radioNetdev) Accept(fd int) (int, netip.AddrPort, error) { return radioBerkeley.Accept(fd) }

func (radioNetdev) Send(fd int, buf []byte, flags int, deadline time.Time) (int, error) {
	return radioBerkeley.Send(fd, buf, flags, deadline)
}

func (radioNetdev) Recv(fd int, buf []byte, flags int, deadline time.Time) (int, error) {
	return radioBerkeley.Recv(fd, buf, flags, deadline)
}

func (radioNetdev) Close(fd int) error { return radioBerkeley.Close(fd) }

func (radioNetdev) SetSockOpt(fd, level, opt int, value interface{}) error {
	return radioBerkeley.SetSockOpt(fd, level, opt, value)
}
