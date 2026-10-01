//go:build esp32

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"device/esp"
	"encoding/hex"
	"machine"
	"net/netip"
	"strconv"
	"time"

	"github.com/soypat/lneto/tcp"

	"claudecontrol/firmware/internal/hello"
)

// Development overrides until the Hub exists: where the agent is and who it
// is, baked in with -ldflags -X. With both set, they win over the record's
// cache so a test bench never chases a stale address.
var (
	devAgentAddr string
	devAgentID   string
)

// The link runs on the main goroutine as a state machine polled from
// pollLine: after WiFi is up the heap has ~20 KB left and a goroutine costs
// 16 KB of it, so lneto's TCP connection is driven directly with static
// buffers instead of the net package plus a reader goroutine.
const (
	linkDialTimeout  = 5 * time.Second
	linkHelloTimeout = 5 * time.Second
	linkIdleTimeout  = 10 * time.Second // the agent sends every 2 s
	linkRetryPause   = 5 * time.Second
	serialAckPeriod  = 2 * time.Second // keeps a USB host's echo timer quiet while frames come over WiFi

	linkRxBufSize = 2048 // TCP receive window: a frame (~3.5 KB) arrives in two
	linkTxBufSize = 512  // HELLO (~130 bytes), CMD and echo lines
	linkTxQueue   = 2    // unacknowledged segments in flight

	ephemeralPortBase = 49152
	ephemeralPortSpan = 16384
	agentAddrsStored  = 3
	rngWordBytes      = 4
)

type linkPhase uint8

const (
	linkDown     linkPhase = iota
	linkDialing            // SYN out, waiting for the handshake
	linkGreeting           // HELLO out, waiting for WELCOME
	linkUp
)

var (
	linkConn    tcp.Conn
	linkRx      [linkRxBufSize]byte
	linkTx      [linkTxBufSize]byte
	linkLine    [lineBufSize]byte // a frame being assembled; serial has its own
	linkLineLen int

	phase        linkPhase
	linkNext     time.Time // when the next dial may start
	linkDeadline time.Time // handshake deadline, then the idle deadline
	linkTarget   int       // which cached address to try next
	linkAddr     string    // address of the connection in progress
	linkAgentID  string    // agent id the token was derived for

	// tcpFrame remembers which link delivered the frame the core is about
	// to answer, so the echo goes back the same way.
	tcpFrame bool

	lastSerialAck time.Time
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

// startLink arms the dialer; called once, after the radio is up.
func startLink() {
	ensureIdentity()
	phase = linkDown
	linkNext = time.Now()
}

// linkStep advances the connection state machine; it never blocks.
func linkStep(now time.Time) {
	switch phase {
	case linkDown:
		if radioUp && !now.Before(linkNext) {
			linkDial(now)
		}
	case linkDialing:
		switch st := linkConn.State(); {
		case st == tcp.StateEstablished:
			id := hex.EncodeToString(settingsRecord.DeviceID[:])
			if !writeTCP(hello.Line(id, fwVersion, deviceToken(linkAgentID))) {
				linkDrop(now)

				return
			}
			phase = linkGreeting
			linkDeadline = now.Add(linkHelloTimeout)
		case st == tcp.StateSynSent || st == tcp.StateSynRcvd || linkConn.AwaitingSynSend():
			if now.After(linkDeadline) {
				linkDrop(now)
			}
		default:
			// Refused or reset: the agent is not there (yet).
			linkDrop(now)
		}
	case linkGreeting, linkUp:
		if linkConn.State().IsClosed() || now.After(linkDeadline) {
			linkDrop(now)
		}
	}
}

// linkDial starts a connection attempt towards the next known address.
func linkDial(now time.Time) {
	linkNext = now.Add(linkRetryPause)

	addrs, agentID := agentTargets()
	if len(addrs) == 0 {
		return
	}
	linkTarget %= len(addrs)
	addr := addrs[linkTarget]
	linkTarget++

	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().Is4() {
		return
	}
	err = linkConn.Configure(tcp.ConnConfig{
		RxBuf:             linkRx[:],
		TxBuf:             linkTx[:],
		TxPacketQueueSize: linkTxQueue,
		RWBackoff:         radioBackoff,
	})
	if err != nil {
		println("link: configure:", err.Error())

		return
	}
	stack := radioStack.LnetoStack()
	port := uint16(ephemeralPortBase + stack.Prand32()%ephemeralPortSpan)
	if err := stack.DialTCP(&linkConn, port, ap); err != nil {
		// ARP still resolving, or no free TCP slot yet: try again later.
		println("link: dial:", err.Error())

		return
	}

	linkAddr, linkAgentID = addr, agentID
	phase = linkDialing
	linkDeadline = now.Add(linkDialTimeout)
}

// linkDrop abandons the current connection and schedules the next attempt.
func linkDrop(now time.Time) {
	linkConn.Abort()
	phase = linkDown
	linkLineLen = 0
	linkNext = now.Add(linkRetryPause)
}

// linkReadLine returns the next complete line received over TCP. The
// WELCOME line is consumed here; frames are handed to the caller.
func linkReadLine(now time.Time) (string, bool) {
	if phase < linkGreeting {
		return "", false
	}
	for {
		if line, ok := takeLinkLine(); ok {
			return linkHandle(line, now)
		}
		if linkConn.BufferedInput() == 0 {
			return "", false
		}
		if linkLineLen >= len(linkLine) {
			linkLineLen = 0 // an oversized line can only be garbage
		}
		n, err := linkConn.Read(linkLine[linkLineLen:])
		if n > 0 {
			linkLineLen += n
			linkDeadline = now.Add(linkIdleTimeout)
		}
		if err != nil || n == 0 {
			return "", false
		}
	}
}

// takeLinkLine cuts the first complete line out of the assembly buffer.
func takeLinkLine() (string, bool) {
	for i := 0; i < linkLineLen; i++ {
		if linkLine[i] != '\n' {
			continue
		}
		end := i
		if end > 0 && linkLine[end-1] == '\r' {
			end--
		}
		line := string(linkLine[:end])
		linkLineLen = copy(linkLine[:], linkLine[i+1:linkLineLen])

		return line, true
	}

	return "", false
}

func linkHandle(line string, now time.Time) (string, bool) {
	if phase == linkUp {
		return line, true
	}
	if got, ok := hello.ParseWelcome(line); !ok || got != linkAgentID {
		// Not our agent (or not an agent at all): never trust frames from it.
		linkDrop(now)

		return "", false
	}
	phase = linkUp
	linkDeadline = now.Add(linkIdleTimeout)
	rememberAgent(linkAddr, linkAgentID)

	return "", false
}

// pollLine hands the core the next frame: TCP first, the USB serial port as
// the fallback and the development path.
func pollLine() (string, bool) {
	radioPump()
	now := time.Now()
	linkStep(now)
	if line, ok := linkReadLine(now); ok {
		tcpFrame = true

		return line, true
	}

	if phase == linkUp {
		drainSerialWhileLinked(now)

		return "", false
	}
	line, ok := pollSerialLine()
	if ok {
		tcpFrame = false
	}

	return line, ok
}

// drainSerialWhileLinked keeps one source of truth while WiFi carries the
// frames: whatever a host on the USB cable sends is discarded, but it hears
// "ok wifi" every two seconds for as long as its bytes keep coming, so its
// echo timer never reopens the port. Whole lines cannot be relied on here:
// the UART ring overflows while a WiFi frame is being drawn.
func drainSerialWhileLinked(now time.Time) {
	if machine.Serial.Buffered() == 0 {
		return
	}
	for {
		if _, ok := pollSerialLine(); !ok {
			break
		}
	}
	if now.Sub(lastSerialAck) >= serialAckPeriod {
		lastSerialAck = now
		sendSerialLine("ok wifi")
	}
}

// sendCommand goes to the agent over TCP when connected, else to the serial port.
func sendCommand(cmd string) {
	line := "CMD " + cmd
	if !writeTCP(line) {
		sendSerialLine(line)
	}
}

// sendEcho answers the frame over the link it came from.
func sendEcho(text string) {
	if tcpFrame {
		writeTCP(text)

		return
	}
	sendSerialLine(text)
}

// writeTCP queues a line on the agent connection without blocking; false
// when there is no connection or no room (the line is dropped, the next
// frame brings a fresh echo anyway).
func writeTCP(line string) bool {
	if phase < linkGreeting && !(phase == linkDialing && linkConn.State() == tcp.StateEstablished) {
		return false
	}
	if linkConn.FreeOutput() < len(line)+1 {
		return false
	}
	_, err := linkConn.Write([]byte(line + "\n"))

	return err == nil
}
