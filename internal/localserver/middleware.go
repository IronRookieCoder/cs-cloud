package localserver

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authMiddleware enforces a shared request key when apiKey is non-empty.
// When apiKey is empty (the default) the middleware is a no-op, preserving
// the historical unauthenticated behavior.
//
// Clients may present the key either as `Authorization: Bearer <key>` or
// `X-API-Key: <key>`. Comparison is constant-time to avoid timing oracles.
func authMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if apiKey == "" {
			return next
		}
		expected := []byte(apiKey)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := extractAPIKey(r)
			if len(provided) == 0 || subtle.ConstantTimeCompare(provided, expected) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="cs-cloud"`)
				writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// extractAPIKey pulls the request key from either the Authorization
// (Bearer scheme) or X-API-Key header. Returns nil when absent or malformed.
func extractAPIKey(r *http.Request) []byte {
	if v := r.Header.Get("X-API-Key"); v != "" {
		return []byte(v)
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil
	}
	const scheme = "Bearer "
	if !strings.HasPrefix(h, scheme) {
		return nil
	}
	v := strings.TrimSpace(strings.TrimPrefix(h, scheme))
	if v == "" {
		return nil
	}
	return []byte(v)
}
