//go:build esp32

package main

import (
	"net/netip"
	"time"

	"github.com/soypat/lneto"
	"github.com/soypat/lneto/x/xnet"
	"tinygo.org/x/espradio"

	"claudecontrol/firmware/internal/provision"
)

// The radio is brought up in steps because espradio's all-in-one NetConnect
// cannot scan before it connects and cannot be called twice. Enable/Start
// happen once per boot; Scan any number of times; Connect once — a refused
// association is retried only by rebooting (see connectScreen).
const (
	radioHostname = "gopherclaude"
	radioPollTime = 5 * time.Millisecond

	// lneto limits, the spike's values; the data link of stage A3 uses one
	// outbound TCP connection plus the Hub's HTTP calls.
	radioMaxTCPPorts  = 2
	radioMaxUDPPorts  = 2
	radioPassivePeers = 64
	radioTCPPool      = 4
	radioTCPQueue     = 4
	radioTCPTxBuf     = 4096
	radioTCPRxBuf     = 1024
	radioTCPTimeout   = 2 * time.Second
)

var (
	radioStarted bool
	radioUp      bool
	radioIP      netip.Addr

	radioDev      *espradio.NetDev
	radioStack    *espradio.Stack
	radioGo       xnet.StackGo
	radioBerkeley xnet.StackBerkeley

	radioBackoff = lneto.BackoffStrategy(func(_ uint) time.Duration { return radioPollTime })
)

// radioCall runs f on a fresh goroutine and waits for it. The WiFi blob
// needs several KB of stack below its caller; the boot and settings screens
// sit deep in the main goroutine (8 KB stacks) and overflowed it on the
// first run, which showed up as a corrupted scheduler a few frames later.
func radioCall(f func()) {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	<-done
}

// radioStart powers the radio once; later calls are no-ops.
func radioStart() error {
	if radioStarted {
		return nil
	}
	if err := espradio.Enable(espradio.Config{Logging: espradio.LogLevelError}); err != nil {
		return err
	}
	if err := espradio.Start(); err != nil {
		return err
	}
	radioStarted = true

	return nil
}

// radioScan lists the access points in range as the provisioning list wants
// them (name and signal only: espradio reports no security type).
func radioScan() ([]provision.Network, error) {
	if err := radioStart(); err != nil {
		return nil, err
	}
	aps, err := espradio.Scan()
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
	radioGo = radioStack.LnetoStack().StackGo(radioBackoff, xnet.StackGoConfig{
		ListenerPoolConfig: xnet.TCPPoolConfig{
			PoolSize:           radioTCPPool,
			QueueSize:          radioTCPQueue,
			TxBufSize:          radioTCPTxBuf,
			RxBufSize:          radioTCPRxBuf,
			EstablishedTimeout: radioTCPTimeout,
			ClosingTimeout:     radioTCPTimeout,
			NewBackoff:         func() lneto.BackoffStrategy { return radioBackoff },
		},
	})
	radioBerkeley = *xnet.NewBerkeleyStack(radioGo.Socket)
	go radioPump()

	res, err := radioStack.SetupWithDHCP(espradio.DHCPConfig{})
	if err != nil {
		return netip.Addr{}, err
	}
	radioIP = netip.AddrFrom4(res.AssignedAddr4)
	radioUp = true

	return radioIP, nil
}

// radioPump moves frames between the WiFi driver and the IP stack; it is the
// only goroutine that may touch the driver's TX path.
func radioPump() {
	for {
		send, recv, _ := radioStack.RecvAndSend()
		if send == 0 && recv == 0 {
			time.Sleep(radioPollTime)
		}
	}
}
