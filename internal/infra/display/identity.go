// Package display is the agent's side of the WiFi display: the TCP listener
// the displays dial, the HELLO/WELCOME handshake, the agent's own identity
// and the pair store the handshake checks against.
package display

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/dethlex/GopherClaude/internal/infra/atomicfile"
)

// Hex lengths of the identifiers on the wire.
const (
	IDLen         = 32 // 16 random bytes
	TokenLen      = 64 // HMAC-SHA256
	keyBytes      = 16
	tokBytes      = 32
	stateFileMode = 0o600
)

// Identity is this agent as the Hub and the displays know it: a random id
// and a bearer token, created on first run.
type Identity struct {
	ID    string `json:"agent_id"`
	Token string `json:"agent_token"`
}

// LoadOrCreateIdentity reads the identity file or creates one.
func LoadOrCreateIdentity(path string) (Identity, error) {
	var id Identity

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &id); err != nil {
			return Identity{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if len(id.ID) == IDLen && len(id.Token) == TokenLen {
			return id, nil
		}

		return Identity{}, fmt.Errorf("parse %s: malformed identity", path)
	case errors.Is(err, fs.ErrNotExist):
	default:
		return Identity{}, err
	}

	id = Identity{ID: randomHex(keyBytes), Token: randomHex(tokBytes)}
	if err := atomicfile.WriteJSON(path, id, stateFileMode); err != nil {
		return Identity{}, err
	}

	return id, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing on macOS means the process is in deep trouble;
		// there is no sensible fallback for an identity.
		panic("crypto/rand: " + err.Error())
	}

	return hex.EncodeToString(b)
}
