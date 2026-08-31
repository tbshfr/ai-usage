package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func newAuthedServer(t *testing.T) (*httptest.Server, *auth.Dashboard) {
	t.Helper()
	dash := auth.NewDashboard("admin", "s3cret", mustSessions(t))
	srv := httptest.NewServer(NewAuthed(seedtest.DB(t), dash))
	t.Cleanup(srv.Close)
	return srv, dash
}

func mustSessions(t *testing.T) *auth.Sessions {
	t.Helper()
	s, err := auth.NewSessions()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var noRedirect = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (int, string, *http.Response) {
	t.Helper()
	reader := strings.NewReader(body)
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := readAll(t, resp)
	return resp.StatusCode, b, resp
}

func login(t *testing.T, srv *httptest.Server, user, pass string) (int, *http.Response) {
	t.Helper()
	form := url.Values{"username": {user}, "password": {pass}}
	req, err := http.NewRequest("POST", srv.URL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	readAll(t, resp)
	return resp.StatusCode, resp
}

func TestLoginRequiredForPages(t *testing.T) {
	srv, _ := newAuthedServer(t)
	for _, p := range []string{"/", "/breakdowns", "/sessions", "/fragments/dashboard-stats"} {
		status, _, resp := do(t, srv, "GET", p, "", nil)
		if status != http.StatusSeeOther {
			t.Errorf("%s: status = %d, want 303", p, status)
		}
		if loc := resp.Header.Get("Location"); loc != "/login" {
			t.Errorf("%s: Location = %q, want /login", p, loc)
		}
	}
}

func TestStaticReachableWithoutLogin(t *testing.T) {
	srv, _ := newAuthedServer(t)
	status, _, _ := do(t, srv, "GET", "/static/app.css", "", nil)
	if status != http.StatusOK {
		t.Errorf("static status = %d, want 200", status)
	}
}

func TestLoginPageRenders(t *testing.T) {
	srv, _ := newAuthedServer(t)
	status, body, _ := do(t, srv, "GET", "/login", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	wantContains(t, body, "Username", "Password", `action="/login"`, `type="password"`)
}

func TestLoginPageRedirectsLoggedIn(t *testing.T) {
	srv, _ := newAuthedServer(t)
	_, resp := login(t, srv, "admin", "s3cret")
	cookie := sessionCookie(t, resp)
	status, _, _ := do(t, srv, "GET", "/login", "", map[string]string{"Cookie": cookie})
	if status != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 to /", status)
	}
}

func sessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie in response")
	return ""
}

func TestLoginBadCredentials(t *testing.T) {
	srv, _ := newAuthedServer(t)
	status, resp := login(t, srv, "admin", "wrong")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Error("failed login must not set a session cookie")
		}
	}
	_, body, _ := do(t, srv, "GET", "/login", "", nil)
	wantContains(t, body, "Sign in")
}

func TestLoginLogoutFlow(t *testing.T) {
	srv, _ := newAuthedServer(t)

	status, resp := login(t, srv, "admin", "s3cret")
	if status != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", status)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("login Location = %q, want /", loc)
	}
	cookie := sessionCookie(t, resp)

	status, body, _ := do(t, srv, "GET", "/?"+fullRangeQuery, "", map[string]string{"Cookie": cookie})
	if status != http.StatusOK {
		t.Fatalf("overview after login: status = %d, want 200", status)
	}
	wantContains(t, body, "Sign out", `href="/logout"`)

	status, _, resp = do(t, srv, "GET", "/logout", "", map[string]string{"Cookie": cookie})
	if status != http.StatusSeeOther {
		t.Errorf("logout status = %d, want 303", status)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}

	// The session is stateless: the expired cookie in the logout response
	// is what ends the browser session. Without it, access is refused.
	status, _, _ = do(t, srv, "GET", "/", "", nil)
	if status != http.StatusSeeOther {
		t.Errorf("after logout: status = %d, want 303", status)
	}
}

func TestNoAuthServerHasNoLoginRoutes(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()
	status, _, _ := do(t, srv, "GET", "/login", "", nil)
	if status != http.StatusNotFound {
		t.Errorf("/login without auth: status = %d, want 404", status)
	}
}

func TestSessionCookieIsSecure(t *testing.T) {
	srv, _ := newAuthedServer(t)
	_, resp := login(t, srv, "admin", "s3cret")
	for _, c := range resp.Cookies() {
		if c.Name != auth.SessionCookie {
			continue
		}
		if !c.Secure {
			t.Error("session cookie not Secure")
		}
		if !c.HttpOnly {
			t.Error("session cookie not HttpOnly")
		}
		return
	}
	t.Fatal("no session cookie in response")
}

func TestLoginRateLimited(t *testing.T) {
	srv, _ := newAuthedServer(t)
	for i := 0; i < failedLoginMax; i++ {
		status, _ := login(t, srv, "admin", "wrong")
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, status)
		}
	}
	status, resp := login(t, srv, "admin", "wrong")
	if status != http.StatusTooManyRequests {
		t.Errorf("status after %d failures = %d, want 429", failedLoginMax, status)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
	// Correct credentials do not bypass the limiter: the account is locked
	// out until the window drains.
	status, _ = login(t, srv, "admin", "s3cret")
	if status != http.StatusTooManyRequests {
		t.Errorf("correct credentials during lockout: status = %d, want 429", status)
	}
}

func TestLoginLimiterReset(t *testing.T) {
	l := &loginLimiter{}
	for i := 0; i < failedLoginMax; i++ {
		l.recordFailure()
	}
	if _, blocked := l.blocked(); !blocked {
		t.Fatal("limiter did not block after max failures")
	}
	l.reset()
	if _, blocked := l.blocked(); blocked {
		t.Error("limiter still blocked after reset")
	}
}
