package api

import (
	"net/http"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

// The security headers go out on every dashboard-port response: pages,
// JSON API answers, and probes alike.
func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	srv := newServer(t, seedtest.DB(t), nil)
	for _, p := range []string{"/", "/api/stats", "/health", "/robots.txt"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := map[string]string{
			"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "no-referrer",
			"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		}
		for h, v := range want {
			if got := resp.Header.Get(h); got != v {
				t.Errorf("%s: %s = %q, want %q", p, h, got, v)
			}
		}
	}
}
