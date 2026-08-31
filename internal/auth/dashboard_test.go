package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestDashboardMiddlewarePublicPaths(t *testing.T) {
	d := NewDashboard("admin", "pw", mustSessions(t))
	for _, p := range []string{"/login", "/logout", "/health", "/ready", "/static/app.css", "/static/vendor/htmx.min.js"} {
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		called := false
		d.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).ServeHTTP(rec, req)
		if !called {
			t.Errorf("public path %s was blocked", p)
		}
	}
}

func TestDashboardMiddlewareRedirectsHTML(t *testing.T) {
	d := NewDashboard("admin", "pw", mustSessions(t))
	for _, p := range []string{"/", "/sessions", "/fragments/dashboard-stats"} {
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		d.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("protected path %s reached the handler", p)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("path %s: status = %d, want 303", p, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Errorf("path %s: Location = %q, want /login", p, loc)
		}
	}
}

func TestDashboardMiddlewareAPIJSON401(t *testing.T) {
	d := NewDashboard("admin", "pw", mustSessions(t))
	req := httptest.NewRequest("GET", "/api/summary", nil)
	rec := httptest.NewRecorder()
	d.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("protected path reached the handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"status":401`) {
		t.Errorf("body = %q, want JSON 401 shape", body)
	}
}

func TestDashboardMiddlewareValidSessionPasses(t *testing.T) {
	d := NewDashboard("admin", "pw", mustSessions(t))
	rec := httptest.NewRecorder()
	d.Sessions().Issue(rec)
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	called := false
	d.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).ServeHTTP(rec2, req)
	if !called {
		t.Error("valid session was redirected")
	}
}

func TestDashboardCheck(t *testing.T) {
	d := NewDashboard("admin", "hunter2", mustSessions(t))
	if !d.Check("admin", "hunter2") {
		t.Error("correct credentials rejected")
	}
	for _, tc := range []struct{ user, pass string }{
		{"admin", "wrong"},
		{"root", "hunter2"},
		{"Admin", "hunter2"},
		{"", ""},
		{"admin", ""},
	} {
		if d.Check(tc.user, tc.pass) {
			t.Errorf("credentials %q/%q accepted", tc.user, tc.pass)
		}
	}
}

func TestGRPCInterceptor(t *testing.T) {
	inter := GRPCUnaryInterceptor(testLogger(), "s3cret")
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Export"}

	t.Run("valid token", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer s3cret"))
		got, err := inter(ctx, nil, info, handler)
		if err != nil || got != "ok" {
			t.Errorf("got %v, %v; want ok, nil", got, err)
		}
	})
	t.Run("missing metadata", func(t *testing.T) {
		_, err := inter(context.Background(), nil, info, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v, want Unauthenticated", status.Code(err))
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer nope"))
		_, err := inter(ctx, nil, info, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v, want Unauthenticated", status.Code(err))
		}
	})
	t.Run("wrong scheme", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Basic czNjcmV0"))
		_, err := inter(ctx, nil, info, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("code = %v, want Unauthenticated", status.Code(err))
		}
	})
}

func mustSessions(t *testing.T) *Sessions {
	t.Helper()
	s, err := NewSessions()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
