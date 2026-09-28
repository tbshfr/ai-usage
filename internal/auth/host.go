package auth

import (
	"net"
	"net/http"
	"strings"

	"github.com/tbshfr/ai-usage"
)

// LoopbackHost protects an unauthenticated dashboard from DNS rebinding.
// Check the literal request authority, never DNS or forwarded headers: an
// attacker-controlled hostname may resolve to loopback and appear same-origin.
// Read-only probes and static assets remain public on any host.
func LoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if assets.ServePublic(w, r) {
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if r.URL.Path == "/health" || r.URL.Path == "/ready" || strings.HasPrefix(r.URL.Path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}
		}
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		ip := net.ParseIP(host)
		if !strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "unauthenticated dashboard requires a localhost or loopback IP host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
