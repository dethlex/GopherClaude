package main

import "machine"

// machine.Serial on the Gopher Badge is USB CDC: it is initialized before
// main() and ReadByte never blocks (returns an error when the RX ring is
// empty). The driver's RX ring holds 512 bytes and drops what does not fit,
// so the main loop drains it on every pass and the host paces its writes
// to that. lineBuf assembles one frame; the worst case (32 sessions with
// the longest texts) is about 3.5 KB.
const lineBufSize = 4096

var (
	lineBuf [lineBufSize]byte
	lineLen int
)

// pollSerialLine drains the USB CDC RX buffer and returns a complete line
// (without the trailing \r\n) once one has arrived; otherwise ("", false).
func pollSerialLine() (string, bool) {
	for machine.Serial.Buffered() > 0 {
		b, err := machine.Serial.ReadByte()
		if err != nil {
			break
		}

		switch b {
		case '\r':
			// Tolerate both \n and \r\n line endings.
		case '\n':
			line := string(lineBuf[:lineLen])
			lineLen = 0

			return line, true
		default:
			if lineLen < lineBufSize {
				lineBuf[lineLen] = b
				lineLen++
			}
		}
	}

	return "", false
}

// sendCommand reports a button action back to the host agent, e.g.
// "CMD focus\n". Writes are dropped while no host has the port open, which is
// fine — a command only matters when the agent is listening.
func sendCommand(cmd string) {
	machine.Serial.Write([]byte("CMD " + cmd + "\n"))
}
