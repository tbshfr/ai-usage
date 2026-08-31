package web

import (
	"net/http"
	"time"
)

// bruteForceDelay slows repeated failed logins; successful logins and
// the plain form are not delayed.
const bruteForceDelay = 500 * time.Millisecond

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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}
	if s.dash.Check(r.PostFormValue("username"), r.PostFormValue("password")) {
		s.dash.Sessions().Issue(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	time.Sleep(bruteForceDelay)
	s.renderLoginError(w, "Wrong username or password.")
}

func (s *server) renderLoginError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
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
