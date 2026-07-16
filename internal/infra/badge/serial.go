package badge

import (
	"bytes"
	"errors"
	"fmt"
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

	defaultPortGlob = "/dev/cu.usbmodem*"
	autoPort        = "auto"

	echoReadTimeout = 50 * time.Millisecond
	echoBufSize     = 256

	// Drop a partial reply line that grows past this without a newline —
	// it can only be noise from a half-open port.
	maxRxAccum = 512
)

var errNoPort = errors.New("no usb serial port found")

// SerialSink writes frames to the badge. The port is (re)opened lazily: the
// badge re-enumerates after flashing and may change its device name, so every
// failure drops the handle and the next send rediscovers the port.
type SerialSink struct {
	requested string
	logger    *slog.Logger

	port  serial.Port
	path  string
	rxBuf []byte // carries a partial reply line between Sends
}

func NewSerialSink(port string, logger *slog.Logger) *SerialSink {
	return &SerialSink{
		requested: port,
		logger:    logger.With("module", "serial"),
	}
}

func (s *SerialSink) Send(snapshot domain.Snapshot) ([]domain.Command, error) {
	if s.port == nil {
		if err := s.open(); err != nil {
			return nil, err
		}
	}

	line := Encode(snapshot, time.Now()) + "\n"

	if _, err := s.port.Write([]byte(line)); err != nil {
		s.drop()

		return nil, fmt.Errorf("write to %q: %w", s.path, err)
	}

	return s.readReplies(), nil
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

	port, err := serial.Open(path, &serial.Mode{BaudRate: baudRate})
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}

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

	matches, err := filepath.Glob(defaultPortGlob)
	if err != nil {
		return "", fmt.Errorf("glob %q: %w", defaultPortGlob, err)
	}

	if len(matches) == 0 {
		return "", errNoPort
	}

	sort.Strings(matches)

	if len(matches) > 1 {
		s.logger.Warn("multiple usb serial ports, using first",
			"chosen", matches[0],
			"all", strings.Join(matches, ","),
		)
	}

	return matches[0], nil
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
func (s *SerialSink) readReplies() []domain.Command {
	buf := make([]byte, echoBufSize)

	n, err := s.port.Read(buf)
	if err == nil && n > 0 {
		s.rxBuf = append(s.rxBuf, buf[:n]...)
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
			s.logger.Debug("badge", "echo", line)
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
