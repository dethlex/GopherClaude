package display

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/usecase"
)

func startTestListener(t *testing.T, accept bool, devices ...Device) (*Listener, *usecase.Fanout, *PairStore, string) {
	t.Helper()
	pairs, _ := LoadPairStore(filepath.Join(t.TempDir(), "devices.json"))
	for _, d := range devices {
		pairs.Upsert(d)
	}
	fan := usecase.NewFanout(slog.Default())
	l := NewListener(ListenerConfig{Addr: "127.0.0.1:0", AgentID: testAgent, Pairs: pairs, Fanout: fan, AcceptUnpaired: accept, Logger: slog.Default()})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return l, fan, pairs, addr.String()
}

func dialHello(t *testing.T, addr, id, token string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte("HELLO " + id + " v-test " + token + "\n")); err != nil {
		t.Fatal(err)
	}
	return c, bufio.NewReader(c)
}

func waitMembers(t *testing.T, fan *usecase.Fanout, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fan.Members()) == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("members %v, want %d", fan.Members(), n)
}

func TestListenerHandshakeFramesAndCommands(t *testing.T) {
	_, fan, pairs, addr := startTestListener(t, false, Device{ID: testID, Token: testTok, Name: "Desk"})
	c, r := dialHello(t, addr, testID, testTok)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	welcome, err := r.ReadString('\n')
	if err != nil || welcome != WelcomeLine(testAgent) {
		t.Fatalf("welcome %q err %v", welcome, err)
	}
	waitMembers(t, fan, 1)

	// A frame goes out on Send; the display answers with an echo and a command.
	done := make(chan []domain.Command, 1)
	go func() {
		cmds, _ := fan.Send(domain.Snapshot{Chats: 2})
		done <- cmds
	}()
	frame, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(frame, "CC9|2|") {
		t.Fatalf("frame %q err %v", frame, err)
	}
	c.Write([]byte("ok chats=2 wait=0\nCMD focus 1\n"))
	var cmds []domain.Command
	select {
	case cmds = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not return")
	}
	// The reply lands after that Send returned; a later Send collects it.
	for i := 0; i < 50 && len(cmds) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		cmds, _ = fan.Send(domain.Snapshot{Chats: 2})
		r.ReadString('\n') // the frame that Send wrote
	}
	if len(cmds) != 1 || cmds[0].Index != 1 {
		t.Fatalf("commands %+v", cmds)
	}

	if d := pairs.Devices()[0]; !strings.HasPrefix(d.LastAddr, "127.0.0.1:") {
		t.Fatalf("last addr not recorded: %+v", d)
	}
}

func TestListenerRejectsUnknownToken(t *testing.T) {
	_, fan, _, addr := startTestListener(t, false, Device{ID: testID, Token: testTok})
	c, r := dialHello(t, addr, testID, strings.Repeat("0", TokenLen))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := r.ReadString('\n'); err == nil {
		t.Fatalf("got %q, want the connection closed without WELCOME", line)
	}
	if len(fan.Members()) != 0 {
		t.Fatalf("members %v", fan.Members())
	}
	// Garbage instead of HELLO is closed as well.
	g, _ := net.DialTimeout("tcp", addr, time.Second)
	defer g.Close()
	g.Write([]byte("CC9|1|0|\n"))
	g.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := bufio.NewReader(g).ReadString('\n'); err == nil {
		t.Fatal("garbage greeting accepted")
	}
}

func TestListenerAcceptsUnpairedWhenAllowed(t *testing.T) {
	_, fan, pairs, addr := startTestListener(t, true)
	c, r := dialHello(t, addr, testID, testTok)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if w, err := r.ReadString('\n'); err != nil || w != WelcomeLine(testAgent) {
		t.Fatalf("welcome %q err %v", w, err)
	}
	waitMembers(t, fan, 1)
	tok, ok := pairs.Token(testID)
	if !ok || tok != testTok {
		t.Fatalf("device not recorded: %q %v", tok, ok)
	}
}

func TestListenerLimit(t *testing.T) {
	_, fan, _, addr := startTestListener(t, true)
	var conns []net.Conn
	for i := 0; i < MaxDisplays; i++ {
		id := strings.Repeat("a", IDLen-2) + string(rune('0'+i/10)) + string(rune('0'+i%10))
		c, r := dialHello(t, addr, id, testTok)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("display %d refused: %v", i, err)
		}
		conns = append(conns, c)
	}
	waitMembers(t, fan, MaxDisplays)
	extra, r := dialHello(t, addr, strings.Repeat("b", IDLen), testTok)
	extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("ninth display accepted")
	}
	// A reconnecting known display replaces its old slot instead of needing a new one.
	again, r2 := dialHello(t, addr, strings.Repeat("a", IDLen-2)+"00", testTok)
	again.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r2.ReadString('\n'); err != nil {
		t.Fatalf("reconnect refused: %v", err)
	}
	waitMembers(t, fan, MaxDisplays)
}

func TestConnSinkWriteDeadline(t *testing.T) {
	// net.Pipe has no buffer: a write blocks until the peer reads, which is
	// exactly a display that stopped draining its socket.
	fan := usecase.NewFanout(slog.Default())
	l := NewListener(ListenerConfig{AgentID: testAgent, Fanout: fan, Logger: slog.Default()})
	server, client := net.Pipe()
	defer client.Close()
	s := newConnSink(server, bufio.NewReader(server), testID, l)
	l.conns[testID] = s
	fan.Add(memberName+testID, s)

	start := time.Now()
	if _, err := s.Send(domain.Snapshot{}); err == nil {
		t.Fatal("write to a stalled display must fail")
	}
	if d := time.Since(start); d < WriteTimeout || d > WriteTimeout+time.Second {
		t.Fatalf("gave up after %v, want about %v", d, WriteTimeout)
	}
	if len(fan.Members()) != 0 {
		t.Fatalf("stalled display was not dropped: %v", fan.Members())
	}
}
