package web

import "net/http"

// SecureHeaders is the exported wrapper used by the api package so the
// whole dashboard port (UI, /api/*, /health) gets the headers.
func SecureHeaders(next http.Handler) http.Handler {
	return secureHeaders(next)
}

// secureHeaders sets the security headers on every dashboard-port response
// (UI pages, /api/*, /health). The dashboard loads everything from its own
// origin — no third parties — so the CSP allows only 'self'.
//
// secureHeaders sets the security headers on every dashboard-port response
// (UI pages, /api/*, /health). The dashboard loads everything from its own
// origin — no third parties — so the CSP allows only 'self'.
//
// Strict-Transport-Security is not set here: the reverse proxy in front of
// this service is responsible for it, so the app must not send a second,
// potentially conflicting value.
func secureHeaders(next http.Handler) http.Handler {
	h := map[string]string{
		"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range h {
			w.Header().Set(k, v)
		}
		next.ServeHTTP(w, r)
	})
}
