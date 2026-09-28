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
// "Authorization: Bearer <token>" accepted by v. The token is never logged.
func Bearer(logger *slog.Logger, v Verifier, next http.Handler) http.Handler {
	return BearerWithHook(logger, v, next, nil)
}

// BearerWithHook additionally calls onReject once per rejected request,
// before the 401 is written, so callers can count transport-level
// rejections without auth depending on any counter package. The hook runs
// after the token check fails; it must be cheap and non-blocking.
//
// Accepted requests carry the token's ID in their context (TokenIDFrom).
// While v does not require authentication, requests pass through
// unattributed.
func BearerWithHook(logger *slog.Logger, v Verifier, next http.Handler, onReject func()) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.Required() {
			next.ServeHTTP(w, r)
			return
		}
		if id, ok := bearerValid(r.Header.Get("Authorization"), v); ok {
			next.ServeHTTP(w, r.WithContext(WithTokenID(r.Context(), id)))
			return
		}
		if onReject != nil {
			onReject()
		}
		logger.Warn("request rejected", "reason", "missing or invalid bearer token", "path", r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Bearer realm="restricted"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func bearerValid(header string, v Verifier) (int64, bool) {
	const prefix = "Bearer "
	value, ok := strings.CutPrefix(header, prefix)
	if !ok {
		return 0, false
	}
	return v.Verify(value)
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
