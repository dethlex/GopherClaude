package badge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.bug.st/serial"

	"github.com/dethlex/GopherClaude/internal/domain"
)

const (
	// CDC ignores the baud rate, but the library requires one.
	baudRate = 115200

	autoPort = "auto"

	echoReadTimeout = 50 * time.Millisecond
	echoBufSize     = 256

	// Drop a partial reply line that grows past this without a newline —
	// it can only be noise from a half-open port.
	maxRxAccum = 512

	// The badge echoes "ok chats=…" for every frame it receives. If nothing
	// comes back for this long the handle is stale — the badge re-enumerated
	// (a USB glitch, a watchdog reset, standby) and macOS keeps accepting
	// writes into the void without an error. Drop the port so the next Send
	// reopens it. ~4 missed 2s frames.
	echoSilenceTimeout = 8 * time.Second

	// badgeErrorPrefix opens the badge's complaint about a frame it dropped
	// ("err: bad frame"), as opposed to the "ok chats=…" echo.
	badgeErrorPrefix = "err:"

	// The badge receives over TinyGo's USB CDC: a 512-byte ring with no
	// flow control (what does not fit is dropped) drained every 10 ms by
	// its main loop. A CC7 frame runs to a few kilobytes, so it goes out
	// in 128-byte pieces 10 ms apart: four chunks fit the 512-byte ring (a
	// 40 ms stall budget); a repaintAll from a button press mid-transmission
	// can still tear one frame, which the next frame repairs within the
	// 10 s link timeout.
	frameChunkSize  = 128
	frameChunkPause = 10 * time.Millisecond
)

var (
	// The badge enumerates as a USB CDC device (usbmodem); the display sits
	// behind a CH340, which macOS names usbserial or wchusbserial depending
	// on the driver in use. The first glob is preferred when both are
	// plugged in: the badge is the primary device of this agent.
	portGlobs = []string{"/dev/cu.usbmodem*", "/dev/cu.usbserial*", "/dev/cu.wchusbserial*"}

	errNoPort = errors.New("no usb serial port found")
)

// SerialSink writes frames to the badge. The port is (re)opened lazily: the
// badge re-enumerates after flashing and may change its device name, so every
// failure drops the handle and the next send rediscovers the port.
type SerialSink struct {
	requested string
	logger    *slog.Logger

	port      serial.Port
	path      string
	rxBuf     []byte    // carries a partial reply line between Sends
	lastReply time.Time // when the badge last sent anything back
}

func NewSerialSink(port string, logger *slog.Logger) *SerialSink {
	return &SerialSink{
		requested: port,
		logger:    logger.With("module", "serial"),
	}
}

func (s *SerialSink) Send(snapshot domain.Snapshot) ([]domain.Command, error) {
	now := time.Now()

	if s.port == nil {
		if err := s.open(); err != nil {
			return nil, err
		}

		s.lastReply = now // grace period before the first echo
	}

	line := Encode(snapshot, now) + "\n"

	if err := writeChunked(s.port, []byte(line), frameChunkSize, func() { time.Sleep(frameChunkPause) }); err != nil {
		s.drop()

		return nil, fmt.Errorf("write to %q: %w", s.path, err)
	}

	cmds := s.readReplies(now)

	// Writes to a stale usb-cdc handle succeed silently on macOS, so a dead
	// badge is only visible as the echo going quiet.
	if now.Sub(s.lastReply) > echoSilenceTimeout {
		s.logger.Warn("no reply from badge, reopening port",
			"silent", now.Sub(s.lastReply).Round(time.Second).String(),
		)
		s.drop()
	}

	return cmds, nil
}

func (s *SerialSink) Close() error {
	if s.port == nil {
		return nil
	}

	err := s.port.Close()
	s.port = nil

	if err != nil {
		return fmt.Errorf("close %q: %w", s.path, err)
	}

	return nil
}

func (s *SerialSink) open() error {
	path, err := s.resolvePath()
	if err != nil {
		return err
	}

	// DTR and RTS stay low from the first instant: the CH340 feeds them to
	// the ESP32's auto-reset circuit, and a driver that asserts them on open
	// reboots the display every time the agent (re)opens the port.
	port, err := serial.Open(path, &serial.Mode{
		BaudRate:          baudRate,
		InitialStatusBits: &serial.ModemOutputBits{RTS: false, DTR: false},
	})
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}

	// The CH340's DTR/RTS lines feed the ESP32's auto-reset circuit; some
	// drivers assert them on open. Release both so opening the port does
	// not reboot the display (a no-op for the badge's CDC port).
	_ = port.SetDTR(false)
	_ = port.SetRTS(false)

	if err := port.SetReadTimeout(echoReadTimeout); err != nil {
		s.logger.Warn("set read timeout", "error", err)
	}

	s.port = port
	s.path = path
	s.logger.Info("port opened", "path", path)

	return nil
}

func (s *SerialSink) resolvePath() (string, error) {
	if s.requested != autoPort && s.requested != "" {
		return s.requested, nil
	}

	var matches []string
	for _, g := range portGlobs {
		found, err := filepath.Glob(g)
		if err != nil {
			return "", fmt.Errorf("glob %q: %w", g, err)
		}
		matches = append(matches, found...)
	}

	chosen := pickPort(matches)
	if chosen == "" {
		return "", errNoPort
	}

	if len(matches) > 1 {
		s.logger.Warn("multiple usb serial ports, using first",
			"chosen", chosen,
			"all", strings.Join(matches, ","),
		)
	}

	return chosen, nil
}

// pickPort chooses among the globbed ports: by glob order (badge first),
// then alphabetically within a kind, so the choice is stable across runs.
func pickPort(matches []string) string {
	for _, g := range portGlobs {
		var kind []string
		for _, m := range matches {
			if ok, _ := filepath.Match(g, m); ok {
				kind = append(kind, m)
			}
		}
		if len(kind) > 0 {
			sort.Strings(kind)

			return kind[0]
		}
	}

	return ""
}

func (s *SerialSink) drop() {
	if s.port == nil {
		return
	}

	if err := s.port.Close(); err != nil {
		s.logger.Debug("close failed port", "error", err)
	}

	s.port = nil
}

// readReplies drains whatever the badge sent back, splits it into lines and
// returns any button commands. Draining also keeps the badge's TX buffer from
// filling; the "ok chats=N wait=M" debug echo is logged, not returned.
func (s *SerialSink) readReplies(now time.Time) []domain.Command {
	buf := make([]byte, echoBufSize)

	n, err := s.port.Read(buf)
	if err == nil && n > 0 {
		s.rxBuf = append(s.rxBuf, buf[:n]...)
		s.lastReply = now
	}

	var cmds []domain.Command

	for {
		idx := bytes.IndexByte(s.rxBuf, '\n')
		if idx < 0 {
			break
		}

		line := strings.TrimSpace(string(s.rxBuf[:idx]))
		s.rxBuf = s.rxBuf[idx+1:]

		switch {
		case line == "":
		case isCommandLine(line):
			if cmd, ok := ParseCommand(line); ok {
				cmds = append(cmds, cmd)
			}
		default:
			if strings.HasPrefix(line, badgeErrorPrefix) {
				// A torn frame (USB overrun while the badge repainted) is
				// worth a warning: many in a row mean the pacing is off.
				s.logger.Warn("badge rejected a frame", "echo", line)
			} else {
				s.logger.Debug("badge", "echo", line)
			}
		}
	}

	if len(s.rxBuf) > maxRxAccum {
		s.rxBuf = s.rxBuf[:0]
	}

	return cmds
}

func isCommandLine(line string) bool {
	_, ok := ParseCommand(line)

	return ok
}

// writeChunked writes data in pieces of at most size bytes and calls pause
// between pieces (not after the last one).
func writeChunked(w io.Writer, data []byte, size int, pause func()) error {
	for start := 0; start < len(data); start += size {
		end := min(start+size, len(data))

		if _, err := w.Write(data[start:end]); err != nil {
			return err
		}

		if end < len(data) {
			pause()
		}
	}

	return nil
}
