//go:build gopher_badge

package main

// The badge has one link: USB serial. Frames come in on it, commands and
// the per-frame echo go out on it.
func pollLine() (string, bool) {
	return pollSerialLine()
}

// sendCommand reports a button action back to the host agent, e.g. "focus".
func sendCommand(cmd string) {
	sendSerialLine("CMD " + cmd)
}

// sendEcho answers a frame ("ok chats=N wait=M") or rejects one ("err: …").
func sendEcho(text string) {
	sendSerialLine(text)
}
