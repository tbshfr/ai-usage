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
	h := Bearer(testLogger(), "s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			h := Bearer(testLogger(), "s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	s.Issue(rec)
	res := rec.Result()
	found := false
	for _, c := range res.Cookies() {
		if c.Name == SessionCookie {
			found = true
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
