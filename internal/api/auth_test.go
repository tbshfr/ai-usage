package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func newAuthedServer(t *testing.T) *httptest.Server {
	t.Helper()
	sessions, err := auth.NewSessions()
	if err != nil {
		t.Fatal(err)
	}
	dash := auth.NewDashboard("admin", "s3cret", sessions)
	srv := httptest.NewServer(NewWithAuth(seedtest.DB(t), testLogger(t), nil, nil, "test", dash))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthAndReadyStayPublic(t *testing.T) {
	srv := newAuthedServer(t)
	for _, p := range []string{"/health", "/ready"} {
		status, body := get(t, srv.URL+p)
		if status != http.StatusOK {
			t.Errorf("%s: status = %d, body %s", p, status, body)
		}
	}
}

func TestAPIUnauthorizedWithoutSession(t *testing.T) {
	srv := newAuthedServer(t)
	for _, p := range []string{"/api/summary", "/api/stats", "/api/generations"} {
		status, body := get(t, srv.URL+p)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", p, status)
		}
		if !strings.Contains(body, `"status":401`) || !strings.Contains(body, `"error":"unauthorized"`) {
			t.Errorf("%s: body = %q, want JSON 401 shape", p, body)
		}
	}
}

func TestDashboardRedirectsUnauthenticated(t *testing.T) {
	srv := newAuthedServer(t)
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}
