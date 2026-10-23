package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/tbshfr/ai-usage"
)

// Dashboard guards everything on the dashboard port with the login
// page. It knows the credentials and owns the session store.
type Dashboard struct {
	user     string
	password string
	sessions *Sessions
}

// NewDashboard builds the dashboard guard. Credentials come from
// config (flags/env); the caller validates they are both non-empty.
func NewDashboard(user, password string, sessions *Sessions) *Dashboard {
	return &Dashboard{user: user, password: password, sessions: sessions}
}

// Sessions exposes the session store for the login/logout handlers.
func (d *Dashboard) Sessions() *Sessions { return d.sessions }

// Check verifies a login form submission. Constant-time: both sides are
// hashed first so length differences never leak through timing.
func (d *Dashboard) Check(user, password string) bool {
	return equalConst(user, d.user) && equalConst(password, d.password)
}

type sessionChecked struct{}

// Middleware allows the public paths through and requires a valid
// session for everything else. HTML routes are redirected to /login;
// /api/* gets a JSON 401 so programmatic clients get a machine-readable
// answer. Public paths never refresh the cookie, so a request to /logout
// cannot re-issue the session it clears.
func (d *Dashboard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve public files directly: their presence must never bypass
		// authentication for a different handler at the same path.
		if assets.ServePublic(w, r) {
			return
		}
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// The dashboard port nests this guard (api.NewWithAuth wraps
		// web.NewAuthed); check and refresh the cookie only once.
		if r.Context().Value(sessionChecked{}) == d.sessions {
			next.ServeHTTP(w, r)
			return
		}
		if d.sessions.Refresh(w, r) {
			// Authenticated responses are per-user: keep shared caches from
			// storing them. Handlers may still tighten this to no-store.
			w.Header().Set("Cache-Control", "private")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionChecked{}, d.sessions)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeUnauthorizedJSON(w)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func publicPath(p string) bool {
	switch p {
	case "/login", "/logout", "/health", "/ready":
		return true
	}
	return strings.HasPrefix(p, "/static/")
}
