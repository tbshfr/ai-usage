package web

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// bruteForceDelay slows repeated failed logins; successful logins and
// the plain form are not delayed.
const bruteForceDelay = 500 * time.Millisecond

// failedLoginWindow and failedLoginMax bound brute-force attempts on the
// dashboard account: more than failedLoginMax failed logins inside the
// window get 429 until the window drains. The limit is global rather than
// per-IP on purpose: per-IP limits are bypassable with spoofed
// X-Forwarded-For values, and the dashboard has exactly one account worth
// attacking. Successful logins clear the counter.
const (
	failedLoginWindow = 15 * time.Minute
	failedLoginMax    = 10
)

type loginLimiter struct {
	mu    sync.Mutex
	fails []time.Time
}

func (l *loginLimiter) blocked() (time.Duration, bool) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	if len(l.fails) < failedLoginMax {
		return 0, false
	}
	return failedLoginWindow - now.Sub(l.fails[0]), true
}

func (l *loginLimiter) recordFailure() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.fails = append(l.fails, now)
}

func (l *loginLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails = nil
}

func (l *loginLimiter) prune(now time.Time) {
	cutoff := now.Add(-failedLoginWindow)
	i := 0
	for i < len(l.fails) && l.fails[i].Before(cutoff) {
		i++
	}
	l.fails = l.fails[i:]
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
	if retry, blocked := s.limiter.blocked(); blocked {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retry.Seconds()))
		s.renderLoginError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if s.dash.Check(r.PostFormValue("username"), r.PostFormValue("password")) {
		s.limiter.reset()
		s.dash.Sessions().Issue(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.limiter.recordFailure()
	time.Sleep(bruteForceDelay)
	s.renderLoginError(w, http.StatusUnauthorized, "Wrong username or password.")
}

func (s *server) renderLoginError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	d := &pageData{Title: "Sign in", Error: msg}
	_ = pageTmpls["login"].ExecuteTemplate(w, "layout", d)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if s.dash == nil {
		http.NotFound(w, r)
		return
	}
	s.dash.Sessions().Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
