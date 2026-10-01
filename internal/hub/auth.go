package hub

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Wire formats: all identifiers are lowercase hex of fixed length, a pairing
// code is six digits.
const (
	IDLen     = 32
	SecretLen = 64
	TokenLen  = 64
	NonceLen  = 32
	CodeLen   = 6

	nonceBytes   = 16
	bearerPrefix = "Bearer "
)

func IsHex(s string, n int) bool {
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

func IsCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// NewNonce is the single-use challenge a display signs in its next heartbeat.
func NewNonce() string {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		// The kernel's random source failing leaves no safe fallback.
		panic("crypto/rand: " + err.Error())
	}

	return hex.EncodeToString(b)
}

func mac(secretHex, message string) string {
	secret, err := hex.DecodeString(secretHex)
	if err != nil || len(secret) == 0 {
		return ""
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(message))

	return hex.EncodeToString(m.Sum(nil))
}

// DeviceSignature is what a registered display sends with each heartbeat:
// HMAC over its id and the nonce from the previous reply.
func DeviceSignature(secretHex, deviceID, nonce string) string {
	return mac(secretHex, deviceID+nonce)
}

// DeviceToken is the HELLO token the agent will expect from the display,
// derived here so the agent learns it at pairing time.
func DeviceToken(secretHex, agentID string) string {
	return mac(secretHex, agentID)
}

// Equal compares secrets in constant time.
func Equal(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// BearerToken extracts a well-formed agent token from the request.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return ""
	}
	tok := strings.TrimSpace(h[len(bearerPrefix):])
	if !IsHex(tok, TokenLen) {
		return ""
	}

	return tok
}

// PublicIP is the client address as the ingress saw it: the first
// X-Forwarded-For entry, else the connection's remote host. An entry that is
// not an IP address yields "" rather than a string an attacker chose.
func PublicIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		ip := net.ParseIP(strings.TrimSpace(first))
		if ip == nil {
			return ""
		}

		return ip.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}

	return host
}

// Limiter allows at most limit events per key within a sliding window.
type Limiter struct {
	limit  int
	window time.Duration

	mu   sync.Mutex
	seen map[string][]time.Time
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, seen: map[string][]time.Time{}}
}

// Allow records an event and reports whether it is within the budget.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.seen[key] = kept

		return false
	}
	l.seen[key] = append(kept, now)

	return true
}
