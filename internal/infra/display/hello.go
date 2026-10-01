package display

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// Connection-level lines, exchanged once per TCP connection before frames.
const (
	helloPrefix   = "HELLO"
	welcomePrefix = "WELCOME"
	helloFields   = 4 // HELLO <device-id> <fw-version> <token>
)

// ParseHello validates "HELLO <device-id> <fw-version> <token>".
func ParseHello(line string) (id, fw, token string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) != helloFields || fields[0] != helloPrefix {
		return "", "", "", false
	}
	id, fw, token = fields[1], fields[2], fields[3]
	if !isLowerHex(id, IDLen) || !isLowerHex(token, TokenLen) {
		return "", "", "", false
	}

	return id, fw, token, true
}

// WelcomeLine is the agent's answer to a good HELLO.
func WelcomeLine(agentID string) string {
	return welcomePrefix + " " + agentID + "\n"
}

// TokenFor derives the display's token the way the firmware does: HMAC-SHA256
// keyed with the device secret over the agent id's hex text.
func TokenFor(secretHex, agentID string) string {
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(agentID))

	return hex.EncodeToString(mac.Sum(nil))
}

// TokenEqual compares tokens in constant time.
func TokenEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}

	return true
}
