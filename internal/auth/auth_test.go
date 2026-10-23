package auth

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(testDiscard{}, nil))
}

type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestBearerAcceptsValidToken(t *testing.T) {
	called := false
	h := Bearer(testLogger(), StaticToken("s3cret"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/v1/traces", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("valid bearer token was rejected")
	}
}

func TestBearerRejectsMissingWrongOrMalformed(t *testing.T) {
	cases := []struct {
		name, header string
	}{
		{"missing", ""},
		{"wrong token", "Bearer nope"},
		{"no prefix", "s3cret"},
		{"basic scheme", "Basic czNjcmV0"},
		{"prefix only", "Bearer "},
		{"extra whitespace", "Bearer  s3cret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := Bearer(testLogger(), StaticToken("s3cret"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/traces", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			h.ServeHTTP(rec, req)
			if called {
				t.Error("request was passed through")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want Bearer challenge", got)
			}
		})
	}
}

func newTestSessions(t *testing.T, ttl time.Duration) *Sessions {
	t.Helper()
	s, err := NewSessions()
	if err != nil {
		t.Fatal(err)
	}
	s.ttl = ttl
	return s
}

func TestSessionIssueAndValid(t *testing.T) {
	s := newTestSessions(t, DefaultSessionTTL)
	rec := httptest.NewRecorder()
	earliestExpiry := time.Now().Add(DefaultSessionTTL).Truncate(time.Second)
	s.Issue(rec)
	latestExpiry := time.Now().Add(DefaultSessionTTL).Truncate(time.Second)
	res := rec.Result()
	found := false
	for _, c := range res.Cookies() {
		if c.Name == SessionCookie {
			found = true
			if c.Expires.Before(earliestExpiry) || c.Expires.After(latestExpiry) {
				t.Errorf("session cookie expiry = %v, want between %v and %v", c.Expires, earliestExpiry, latestExpiry)
			}
			if !c.HttpOnly {
				t.Error("session cookie not HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Error("session cookie SameSite not Lax")
			}
			if c.Path != "/" {
				t.Errorf("session cookie path = %q, want /", c.Path)
			}
		}
	}
	if !found {
		t.Fatal("no session cookie set")
	}
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range res.Cookies() {
		req.AddCookie(c)
	}
	if !s.Valid(req) {
		t.Error("fresh session cookie rejected")
	}
}

func TestSessionExpired(t *testing.T) {
	s := newTestSessions(t, -time.Minute)
	rec := httptest.NewRecorder()
	s.Issue(rec)
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if s.Valid(req) {
		t.Error("expired session accepted")
	}
}

func TestSessionTampered(t *testing.T) {
	s := newTestSessions(t, DefaultSessionTTL)
	rec := httptest.NewRecorder()
	s.Issue(rec)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			cookie = c
		}
	}
	cases := []struct {
		name, value string
	}{
		// The flip targets the 2nd-to-last base64 char: the final char's
		// low 2 bits are dropped when decoding 32 bytes, so flipping it can
		// leave the MAC unchanged (flaky, not actually a tamper).
		{"flipped mac byte", flipByte(cookie.Value, len(cookie.Value)-2)},
		{"flipped expiry byte", flipByte(cookie.Value, 0)},
		{"no dot", strings.ReplaceAll(cookie.Value, ".", "")},
		{"garbage", "garbage.value"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tc.value})
			if s.Valid(req) {
				t.Error("tampered session accepted")
			}
		})
	}
}

func flipByte(v string, i int) string {
	b := []byte(v)
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

func TestSessionClear(t *testing.T) {
	s := newTestSessions(t, DefaultSessionTTL)
	rec := httptest.NewRecorder()
	s.Clear(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Errorf("Clear did not expire the cookie: %+v", cookies)
	}
}

func TestSessionsRejectCookieFromOtherSecret(t *testing.T) {
	a := newTestSessions(t, DefaultSessionTTL)
	b := newTestSessions(t, DefaultSessionTTL)
	rec := httptest.NewRecorder()
	a.Issue(rec)
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if b.Valid(req) {
		t.Error("cookie signed by another secret accepted")
	}
}

func TestConfiguredSessionsSurviveRestart(t *testing.T) {
	secret := strings.Repeat("k", 32)
	a, err := NewConfiguredSessions(secret, time.Hour, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Issue(rec)
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if exp := rec.Result().Cookies()[0].Expires; time.Until(exp) > time.Hour || time.Until(exp) < 59*time.Minute {
		t.Errorf("cookie expiry = %v, want ~1h", exp)
	}

	if !must(t)(NewConfiguredSessions(secret, time.Hour, "admin", "pw")).Valid(req) {
		t.Error("cookie rejected by a new process with the same secret")
	}
	for name, s := range map[string]*Sessions{
		"other secret":   must(t)(NewConfiguredSessions(strings.Repeat("x", 32), time.Hour, "admin", "pw")),
		"other password": must(t)(NewConfiguredSessions(secret, time.Hour, "admin", "pw2")),
		"other user":     must(t)(NewConfiguredSessions(secret, time.Hour, "root", "pw")),
		"shifted creds":  must(t)(NewConfiguredSessions(secret, time.Hour, "adminp", "w")),
		"random secret":  must(t)(NewConfiguredSessions("", time.Hour, "admin", "pw")),
	} {
		if s.Valid(req) {
			t.Errorf("%s: cookie accepted", name)
		}
	}
}

func TestRandomSessionsDoNotSurviveRestart(t *testing.T) {
	a := must(t)(NewSessions())
	rec := httptest.NewRecorder()
	a.Issue(rec)
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if must(t)(NewSessions()).Valid(req) {
		t.Error("cookie accepted by a new process without a configured secret")
	}
}

// must returns a func so it can wrap a two-value call directly.
func must(t *testing.T) func(*Sessions, error) *Sessions {
	t.Helper()
	return func(s *Sessions, err error) *Sessions {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func sessionRequest(rec *httptest.ResponseRecorder, path string) *http.Request {
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func sessionCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			out = append(out, c)
		}
	}
	return out
}

func TestSessionRefreshSlidesExpiry(t *testing.T) {
	s := newTestSessions(t, DefaultSessionTTL)
	issued := httptest.NewRecorder()
	s.issue(issued, time.Now().Add(-2*time.Hour))
	rec := httptest.NewRecorder()
	if !s.Refresh(rec, sessionRequest(issued, "/")) {
		t.Fatal("valid session rejected")
	}
	cookies := sessionCookies(rec)
	if len(cookies) != 1 {
		t.Fatalf("refreshed cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if left := time.Until(c.Expires); left < DefaultSessionTTL-time.Minute || left > DefaultSessionTTL {
		t.Errorf("refreshed expiry in %v, want ~%v", left, DefaultSessionTTL)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("refreshed cookie lost attributes: %+v", c)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private" {
		t.Errorf("Cache-Control = %q, want private", cc)
	}
	if !s.Valid(sessionRequest(rec, "/")) {
		t.Error("refreshed cookie rejected")
	}
}

func TestSessionRefreshThrottled(t *testing.T) {
	for name, ttl := range map[string]time.Duration{"long ttl": DefaultSessionTTL, "short ttl": time.Minute} {
		t.Run(name, func(t *testing.T) {
			s := newTestSessions(t, ttl)
			issued := httptest.NewRecorder()
			s.Issue(issued)
			rec := httptest.NewRecorder()
			if !s.Refresh(rec, sessionRequest(issued, "/")) {
				t.Fatal("valid session rejected")
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Error("fresh cookie re-issued on every request")
			}
		})
	}
	s := newTestSessions(t, time.Minute)
	issued := httptest.NewRecorder()
	s.issue(issued, time.Now().Add(-10*time.Second))
	rec := httptest.NewRecorder()
	s.Refresh(rec, sessionRequest(issued, "/"))
	if len(sessionCookies(rec)) != 1 {
		t.Error("short-TTL session not refreshed after ttl/10")
	}
}

func TestSessionRefreshRejectsInvalid(t *testing.T) {
	s := newTestSessions(t, DefaultSessionTTL)
	expired := httptest.NewRecorder()
	s.issue(expired, time.Now().Add(-DefaultSessionTTL-time.Second))
	other := httptest.NewRecorder()
	newTestSessions(t, DefaultSessionTTL).issue(other, time.Now().Add(-2*time.Hour))
	tampered := httptest.NewRequest("GET", "/", nil)
	tampered.AddCookie(&http.Cookie{Name: SessionCookie, Value: "garbage.value"})
	for name, req := range map[string]*http.Request{
		"no cookie":    httptest.NewRequest("GET", "/", nil),
		"expired":      sessionRequest(expired, "/"),
		"other secret": sessionRequest(other, "/"),
		"tampered":     tampered,
	} {
		rec := httptest.NewRecorder()
		if s.Refresh(rec, req) {
			t.Errorf("%s: session accepted", name)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: cookie issued", name)
		}
	}
}

func TestSessionRefreshShortensAfterTTLReduced(t *testing.T) {
	secret := strings.Repeat("k", 32)
	long := must(t)(NewConfiguredSessions(secret, 30*24*time.Hour, "admin", "pw"))
	issued := httptest.NewRecorder()
	long.Issue(issued)
	short := must(t)(NewConfiguredSessions(secret, 24*time.Hour, "admin", "pw"))
	rec := httptest.NewRecorder()
	if !short.Refresh(rec, sessionRequest(issued, "/")) {
		t.Fatal("valid session rejected")
	}
	cookies := sessionCookies(rec)
	if len(cookies) != 1 || time.Until(cookies[0].Expires) > 24*time.Hour {
		t.Errorf("cookie not shortened to the new TTL: %+v", cookies)
	}
}
