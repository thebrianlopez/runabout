package ws

import (
	"crypto/subtle"
	"net/http"
)

// authMiddleware validates the Bearer token on WebSocket upgrade requests.
// Returns true if the token is valid. Writes the appropriate HTTP status on failure.
func authMiddleware(token string, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return false
	}
	provided := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}
