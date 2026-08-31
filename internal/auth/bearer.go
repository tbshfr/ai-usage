package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// Bearer wraps next so it only answers requests carrying
// "Authorization: Bearer <token>". The token is never logged.
func Bearer(logger *slog.Logger, token string, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerValid(r.Header.Get("Authorization"), token) {
			next.ServeHTTP(w, r)
			return
		}
		logger.Warn("request rejected", "reason", "missing or invalid bearer token", "path", r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Bearer realm="restricted"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func bearerValid(header, token string) bool {
	const prefix = "Bearer "
	value, ok := strings.CutPrefix(header, prefix)
	if !ok {
		return false
	}
	return equalConst(value, token)
}

// equalConst compares two strings without leaking content or length
// through timing: both are hashed to fixed size first.
func equalConst(a, b string) bool {
	h1 := sha256.Sum256([]byte(a))
	h2 := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(h1[:], h2[:]) == 1
}

func writeUnauthorizedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized", "status": http.StatusUnauthorized})
}
