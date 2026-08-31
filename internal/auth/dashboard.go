package auth

import (
	"net/http"
	"strings"
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

// Middleware allows the public paths through and requires a valid
// session for everything else. HTML routes are redirected to /login;
// /api/* gets a JSON 401 so programmatic clients get a machine-readable
// answer.
func (d *Dashboard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if d.sessions.Valid(r) {
			next.ServeHTTP(w, r)
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
	case "/login", "/logout", "/health", "/ready", "/robots.txt":
		return true
	}
	return strings.HasPrefix(p, "/static/")
}
