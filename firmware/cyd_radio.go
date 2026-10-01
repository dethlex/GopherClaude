//go:build esp32

package main

/*
static inline unsigned int gc_intenable_get(void) {
	unsigned int v;
	__asm__ volatile("rsr.intenable %0" : "=a"(v));
	return v;
}

static inline void gc_intenable_set(unsigned int v) {
	__asm__ volatile("wsr.intenable %0; rsync" : : "a"(v));
}
*/
import "C"

import (
	"net/netip"
	"time"

	"github.com/soypat/lneto"
	"tinygo.org/x/espradio"

	"claudecontrol/firmware/internal/provision"
)

// The radio is brought up in steps because espradio's all-in-one NetConnect
// cannot scan before it connects and cannot be called twice. Enable/Start
// happen once per boot; Scan any number of times; Connect once — a refused
// association is retried only by rebooting (see connectScreen).
const (
	radioHostname  = "gopherclaude"
	radioPollTime  = 5 * time.Millisecond
	radioPumpBurst = 8 // frames moved per pump call before yielding to the UI

	// lneto limits, the spike's values; the data link of stage A3 uses one
	// outbound TCP connection plus the Hub's HTTP calls.
	radioMaxTCPPorts  = 2
	radioMaxUDPPorts  = 2
	radioPassivePeers = 8 // ARP neighbours: the gateway and the agent
)

var (
	radioStarted bool
	radioUp      bool
	radioIP      netip.Addr

	// tinygoInterrupts is the CPU INTENABLE mask before the radio started:
	// TinyGo's UART (8) and timer (9) lines. The WiFi blob rewrites the
	// whole register during Start/Connect (observed 0x300 → 0x70000000),
	// which silently kills serial reception; espradio only protects the
	// mask inside its own ISR passes, so we put our bits back ourselves.
	tinygoInterrupts uint32

	radioDev   *espradio.NetDev
	radioStack *espradio.Stack

	radioBackoff = lneto.BackoffStrategy(func(_ uint) time.Duration { return radioPollTime })
)

// radioCall runs f on a fresh goroutine and waits for it. The WiFi blob
// needs several KB of stack below its caller; the boot and settings screens
// sit deep in the main goroutine (8 KB stacks) and overflowed it on the
// first run, which showed up as a corrupted scheduler a few frames later.
// While it waits, the main goroutine keeps answering the host: a connect
// takes seconds, and an agent that hears nothing for 8 s reopens the port,
// which resets the board through the CH340's DTR line.
func radioCall(f func()) {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		radioGuardInterrupts()
		radioPump()
		feedWatchdog()
		drainSerial()
		time.Sleep(touchPoll)
	}
}

// radioGuardInterrupts re-enables TinyGo's CPU interrupt lines when the
// blob has cleared them. Cheap (one special-register read), so it runs after
// every radio operation and from the input poll every 10 ms.
func radioGuardInterrupts() {
	if tinygoInterrupts == 0 {
		return
	}
	cur := uint32(C.gc_intenable_get())
	if cur&tinygoInterrupts != tinygoInterrupts {
		C.gc_intenable_set(C.uint(cur | tinygoInterrupts))
	}
}

// radioStart powers the radio once; later calls are no-ops.
func radioStart() error {
	if radioStarted {
		return nil
	}
	tinygoInterrupts = uint32(C.gc_intenable_get())
	if err := espradio.Enable(espradio.Config{Logging: espradio.LogLevelError}); err != nil {
		return err
	}
	if err := espradio.Start(); err != nil {
		return err
	}
	radioStarted = true
	radioGuardInterrupts()

	return nil
}

// radioScan lists the access points in range as the provisioning list wants
// them (name and signal only: espradio reports no security type).
func radioScan() ([]provision.Network, error) {
	if err := radioStart(); err != nil {
		return nil, err
	}
	aps, err := espradio.Scan()
	radioGuardInterrupts()
	if err != nil {
		return nil, err
	}
	nets := make([]provision.Network, len(aps))
	for i, ap := range aps {
		nets[i] = provision.Network{SSID: ap.SSID, RSSI: ap.RSSI}
	}

	return nets, nil
}

// radioConnect joins the network and brings the IP stack up with DHCP. It
// returns the assigned address; on any error the radio is in an undefined
// state and the caller reboots.
func radioConnect(ssid, password string) (netip.Addr, error) {
	if err := radioStart(); err != nil {
		return netip.Addr{}, err
	}
	if err := espradio.Connect(espradio.STAConfig{SSID: ssid, Password: password}); err != nil {
		return netip.Addr{}, err
	}

	var err error
	if radioDev, err = espradio.StartNetDev(); err != nil {
		return netip.Addr{}, err
	}
	radioStack, err = espradio.NewStack(radioDev, espradio.StackConfig{
		Hostname:         radioHostname,
		MaxTCPPorts:      radioMaxTCPPorts,
		MaxUDPPorts:      radioMaxUDPPorts,
		PassivePeers:     radioPassivePeers,
		AcceptBroadcast4: false, // the AP reflects our own broadcasts back to us
	})
	if err != nil {
		return netip.Addr{}, err
	}

	res, err := radioStack.SetupWithDHCP(espradio.DHCPConfig{})
	if err != nil {
		return netip.Addr{}, err
	}
	radioIP = netip.AddrFrom4(res.AssignedAddr4)
	radioUp = true
	radioGuardInterrupts()

	return radioIP, nil
}

// radioPump moves frames between the WiFi driver and the IP stack. It runs
// on the main goroutine, from pollLine and from radioCall's wait loop,
// instead of on its own: a goroutine stack is 16 KB, and after WiFi is up
// the heap has barely more than that left. Nothing else may touch the
// driver's TX path.
func radioPump() {
	if radioStack == nil {
		return
	}
	for i := 0; i < radioPumpBurst; i++ {
		send, recv, _ := radioStack.RecvAndSend()
		if send == 0 && recv == 0 {
			return
		}
	}
}
