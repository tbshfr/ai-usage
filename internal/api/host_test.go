package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestDashboardHostProtection(t *testing.T) {
	db := seedtest.EmptyDB(t)
	sessions, err := auth.NewSessions()
	if err != nil {
		t.Fatal(err)
	}
	dash := auth.NewDashboard("admin", "secret", sessions)
	issued := httptest.NewRecorder()
	sessions.Issue(issued)
	for _, tc := range []struct {
		name     string
		dash     *auth.Dashboard
		host     string
		loggedIn bool
		want     int
	}{
		{"local unauthenticated", nil, "localhost:8080", false, http.StatusOK},
		{"rebinding", nil, "attacker.example:8080", false, http.StatusForbidden},
		{"custom host with login", dash, "dashboard.example", true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWithAuth(db, testLogger(t), nil, nil, nil, "test", tc.dash)
			for _, path := range []string{"/settings/tokens", "/api/tokens", "/health", "/ready", "/static/app.css", "/favicon.ico"} {
				want := tc.want
				if path == "/health" || path == "/ready" || path == "/static/app.css" || path == "/favicon.ico" {
					want = http.StatusOK
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					r := httptest.NewRequest(method, "http://"+tc.host+path, nil)
					r.Header.Set("Sec-Fetch-Site", "same-origin")
					if tc.loggedIn {
						r.AddCookie(issued.Result().Cookies()[0])
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != want {
						t.Errorf("%s %s: status=%d want=%d", method, path, w.Code, want)
					}
				}
			}
		})
	}
}
