package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host    string
		allowed bool
	}{
		{"localhost", true}, {"localhost:8080", true}, {"LOCALHOST.:8080", true},
		{"127.0.0.1:8080", true}, {"127.0.0.2", true},
		{"[::1]:8080", true}, {"[::1]", true}, {"[::ffff:127.0.0.1]:8080", true},
		{"attacker.example:8080", false}, {"localhost.attacker.example", false},
		{"127.0.0.1.attacker.example:8080", false}, {"localhost@attacker.example", false},
		{"192.168.1.2:8080", false}, {"0.0.0.0:8080", false}, {"[::]:8080", false},
		{"", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			called := false
			h := LoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequest("POST", "http://localhost/tokens", nil)
			r.Host = tc.host
			// Neither an apparent same-origin request nor proxy headers
			// may authorize an attacker-controlled request authority.
			r.Header.Set("Origin", "http://"+tc.host)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-Forwarded-Host", "localhost")
			r.Header.Set("Forwarded", "host=localhost")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusForbidden
			if tc.allowed {
				want = http.StatusNoContent
			}
			if called != tc.allowed || w.Code != want {
				t.Fatalf("host %q: called=%v status=%d, want allowed=%v status=%d", tc.host, called, w.Code, tc.allowed, want)
			}
		})
	}
}

func TestLoopbackHostRejectsWritesToPublicPaths(t *testing.T) {
	h := LoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("non-loopback request reached a write handler")
	}))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/health", "/ready", "/static/app.css", "/favicon.ico"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, "http://attacker.example"+path, nil))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status=%d, want 403", method, path, w.Code)
			}
		}
	}
}
