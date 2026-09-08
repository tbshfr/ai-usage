package web

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bruteForceDelay slows repeated failed logins; successful logins and
// the plain form are not delayed.
const bruteForceDelay = 500 * time.Millisecond

// failedLoginWindow and failedLoginMax bound brute-force attempts on the
// dashboard account: more than failedLoginMax failed logins from one client
// IP inside the window get 429 until the window drains. failedLoginMaxGlobal
// caps failures across all IPs so distributed password spraying is still
// slowed, at a threshold high enough that a single source cannot lock the
// operator out. Successful logins clear the per-IP counter; the global cap
// only decays with the window.
const (
	failedLoginWindow    = 15 * time.Minute
	failedLoginMax       = 3
	failedLoginMaxGlobal = 100
)

// clientIP returns the address to attribute the request to. The dashboard
// runs behind a reverse proxy that appends the connecting client's address
// to X-Forwarded-For, so the rightmost entry is the only one the proxy
// observed; everything to its left is client-controlled. Requests that did
// not come through the proxy fall back to RemoteAddr.
//
// WARNING: this trust is only safe behind an appending proxy (Caddy/nginx
// default). A client connecting directly controls X-Forwarded-For and can
// rotate it to evade the per-IP login limit (see README). Always run behind
// the proxy when non-loopback.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

type loginLimiter struct {
	mu     sync.Mutex
	ips    map[string][]time.Time
	global []time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{ips: map[string][]time.Time{}}
}

func (l *loginLimiter) blocked(ip string) (time.Duration, bool) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	if fails := l.ips[ip]; len(fails) >= failedLoginMax {
		return failedLoginWindow - now.Sub(fails[0]), true
	}
	if len(l.global) >= failedLoginMaxGlobal {
		return failedLoginWindow - now.Sub(l.global[0]), true
	}
	return 0, false
}

func (l *loginLimiter) recordFailure(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.ips[ip] = append(l.ips[ip], now)
	l.global = append(l.global, now)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
}

func (l *loginLimiter) prune(now time.Time) {
	cutoff := now.Add(-failedLoginWindow)
	l.global = pruneFails(l.global, cutoff)
	for ip, fails := range l.ips {
		fails = pruneFails(fails, cutoff)
		if len(fails) == 0 {
			delete(l.ips, ip)
		} else {
			l.ips[ip] = fails
		}
	}
}

func pruneFails(fails []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(fails) && fails[i].Before(cutoff) {
		i++
	}
	return fails[i:]
}

func (s *server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.dash == nil {
		http.NotFound(w, r)
		return
	}
	if s.dash.Sessions().Valid(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", &pageData{Title: "Sign in"})
}

func (s *server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.dash == nil {
		http.NotFound(w, r)
		return
	}
	ip := clientIP(r)
	if retry, blocked := s.limiter.blocked(ip); blocked {
		slog.Warn("login rate limited", "ip", ip, "retry_after", retry.String())
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retry.Seconds()))
		s.renderLoginError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if s.dash.Check(r.PostFormValue("username"), r.PostFormValue("password")) {
		slog.Info("login successful", "ip", ip)
		s.limiter.reset(ip)
		s.dash.Sessions().Issue(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.limiter.recordFailure(ip)
	time.Sleep(bruteForceDelay)
	s.renderLoginError(w, http.StatusUnauthorized, "Wrong username or password.")
}

func (s *server) renderLoginError(w http.ResponseWriter, status int, msg string) {
	d := &pageData{Title: "Sign in", Error: msg}
	renderTemplate(w, pageTmpls["login"], "layout", status, d)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if s.dash == nil {
		http.NotFound(w, r)
		return
	}
	s.dash.Sessions().Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
