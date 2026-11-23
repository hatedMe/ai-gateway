package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// Prefer the client's conversation ID. Hash the complete prepared request as a
// fallback so short, repeated first prompts do not share a session.
func openCodeSession(r *http.Request, body []byte) string {
	for _, name := range []string{"X-Claude-Code-Session-Id", "x-opencode-session", "x-session-id"} {
		if id := r.Header.Get(name); id != "" {
			return id
		}
	}
	sum := sha256.Sum256(body)
	return "gateway_" + hex.EncodeToString(sum[:])
}
