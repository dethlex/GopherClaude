package display

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/dethlex/GopherClaude/internal/domain"
	"github.com/dethlex/GopherClaude/internal/infra/badge"
	"github.com/dethlex/GopherClaude/internal/usecase"
)

// Connection policy.
const (
	MaxDisplays  = 8
	HelloTimeout = 5 * time.Second
	WriteTimeout = 2 * time.Second

	// IdleTimeout: a display echoes every frame; one that said nothing for
	// this long is gone (its WiFi died, or it rebooted and will redial).
	IdleTimeout = 10 * time.Second

	lineBufSize = 8192
	memberName  = "display:"
)

// ListenerConfig wires the listener.
type ListenerConfig struct {
	Addr           string
	AgentID        string
	Pairs          *PairStore
	Fanout         *usecase.Fanout
	AcceptUnpaired bool // development: trust any HELLO and record its token
	Logger         *slog.Logger
}

// Listener accepts displays, runs the handshake and hands each accepted
// connection to the fan-out as a sink.
type Listener struct {
	cfg    ListenerConfig
	logger *slog.Logger

	mu    sync.Mutex
	conns map[string]*connSink // by device id
}

func NewListener(cfg ListenerConfig) *Listener {
	return &Listener{cfg: cfg, logger: cfg.Logger.With("module", "display"), conns: map[string]*connSink{}}
}

// Start binds the address and serves until ctx is cancelled.
func (l *Listener) Start(ctx context.Context) (net.Addr, error) {
	ln, err := net.Listen("tcp", l.cfg.Addr)
	if err != nil {
		return nil, err
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go l.serve(ctx, ln)

	l.logger.Info("listening for displays", "addr", ln.Addr().String())

	return ln.Addr(), nil
}

func (l *Listener) serve(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			l.logger.Debug("accept", "error", err)
			time.Sleep(time.Second)

			continue
		}
		go l.handshake(c)
	}
}

// handshake reads HELLO, checks the token and registers the connection.
func (l *Listener) handshake(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(HelloTimeout))
	r := bufio.NewReaderSize(c, lineBufSize)
	line, err := r.ReadString('\n')
	if err != nil {
		l.logger.Debug("no hello", "from", c.RemoteAddr().String(), "error", err)
		c.Close()

		return
	}

	id, fw, token, ok := ParseHello(line)
	if !ok {
		l.logger.Warn("display sent a malformed greeting", "from", c.RemoteAddr().String())
		c.Close()

		return
	}

	known, paired := l.cfg.Pairs.Token(id)
	switch {
	case paired && TokenEqual(known, token):
	case !paired && l.cfg.AcceptUnpaired:
		l.logger.Warn("accepting an unpaired display (development mode)", "device", id, "from", c.RemoteAddr().String())
		if err := l.cfg.Pairs.Upsert(Device{ID: id, Token: token, PairedAt: time.Now()}); err != nil {
			l.logger.Warn("record device", "device", id, "error", err)
		}
	default:
		l.logger.Warn("display rejected", "device", id, "from", c.RemoteAddr().String(), "paired", paired)
		c.Close()

		return
	}

	l.mu.Lock()
	if _, replacing := l.conns[id]; !replacing && len(l.conns) >= MaxDisplays {
		l.mu.Unlock()
		l.logger.Warn("too many displays, refusing", "device", id)
		c.Close()

		return
	}
	sink := newConnSink(c, r, id, l)
	l.conns[id] = sink
	l.mu.Unlock()

	// The store is updated before WELCOME goes out: once the display has
	// read it, nothing is still being written on its behalf.
	if err := l.cfg.Pairs.Touch(id, c.RemoteAddr().String()); err != nil {
		l.logger.Debug("touch device", "device", id, "error", err)
	}

	c.SetReadDeadline(time.Time{})
	c.SetWriteDeadline(time.Now().Add(WriteTimeout))
	if _, err := c.Write([]byte(WelcomeLine(l.cfg.AgentID))); err != nil {
		l.drop(sink)

		return
	}
	l.cfg.Fanout.Add(memberName+id, sink) // replaces (and closes) a stale connection of the same display
	l.logger.Info("display connected", "device", id, "fw", fw, "from", c.RemoteAddr().String())
	go sink.readLoop()
}

// drop forgets a connection and closes it. A connection the same display
// already replaced (it redialed after a WiFi hiccup) is only closed: the
// fan-out member of that name is the new connection, which must stay.
func (l *Listener) drop(s *connSink) {
	l.mu.Lock()
	current := l.conns[s.id] == s
	if current {
		delete(l.conns, s.id)
	}
	l.mu.Unlock()

	if current {
		l.cfg.Fanout.Remove(memberName + s.id)
		l.logger.Info("display disconnected", "device", s.id)
	}
	s.Close()
}

// connSink is one display connection seen as a domain.Sink: Send writes the
// frame and returns whatever reply lines the reader goroutine collected
// since the last call.
type connSink struct {
	c      net.Conn
	r      *bufio.Reader
	id     string
	parent *Listener

	lines    chan string
	once     sync.Once
	lastSeen time.Time
	mu       sync.Mutex
}

// replyQueue bounds the reply lines kept between two Sends; a display sends
// one echo per frame plus the odd command, so this never fills in practice.
const replyQueue = 64

func newConnSink(c net.Conn, r *bufio.Reader, id string, parent *Listener) *connSink {
	return &connSink{c: c, r: r, id: id, parent: parent, lines: make(chan string, replyQueue), lastSeen: time.Now()}
}

// readLoop turns the connection into lines; it ends when the connection
// does, and then drops the member.
func (s *connSink) readLoop() {
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			s.parent.drop(s)

			return
		}
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		select {
		case s.lines <- line:
		default:
			// The core is behind; a dropped echo is harmless.
		}
	}
}

var errDisplayIdle = errors.New("display idle")

func (s *connSink) Send(snapshot domain.Snapshot) ([]domain.Command, error) {
	now := time.Now()
	frame := badge.Encode(snapshot, now) + "\n"

	s.c.SetWriteDeadline(now.Add(WriteTimeout))
	if _, err := s.c.Write([]byte(frame)); err != nil {
		s.parent.drop(s)

		return nil, err
	}

	var cmds []domain.Command
	for {
		select {
		case line := <-s.lines:
			switch kind, cmd := badge.ClassifyReply(line); kind {
			case badge.ReplyCommand:
				cmds = append(cmds, cmd)
			case badge.ReplyRejected:
				s.parent.logger.Warn("display rejected a frame", "device", s.id)
			case badge.ReplyEcho, badge.ReplyEmpty:
			default:
				s.parent.logger.Debug("display", "device", s.id, "line", line)
			}

			continue
		default:
		}

		break
	}

	s.mu.Lock()
	idle := now.Sub(s.lastSeen)
	s.mu.Unlock()
	if idle > IdleTimeout {
		s.parent.drop(s)

		return nil, errDisplayIdle
	}

	return cmds, nil
}

func (s *connSink) Close() error {
	var err error
	s.once.Do(func() {
		err = s.c.Close()
	})

	return err
}
