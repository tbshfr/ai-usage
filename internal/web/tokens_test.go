package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

var plainTokenRE = regexp.MustCompile(`aiu_[A-Za-z0-9_-]{20,}`)

func newTokenServer(t *testing.T) (*httptest.Server, *auth.TokenStore) {
	t.Helper()
	db := seedtest.DB(t)
	store, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(db, nil, nil, nil, "test", WithTokens(store)))
	t.Cleanup(srv.Close)
	return srv, store
}

func postForm(t *testing.T, srv *httptest.Server, path string, form url.Values, hdr map[string]string) (int, string, *http.Response) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range hdr {
		h[k] = v
	}
	return do(t, srv, "POST", path, form.Encode(), h)
}

func TestTokensPageLifecycle(t *testing.T) {
	srv, store := newTokenServer(t)
	status, body := get(t, srv.URL+"/tokens")
	if status != http.StatusOK {
		t.Fatalf("GET /tokens = %d", status)
	}
	wantContains(t, body, "No tokens yet", `href="/tokens" class="active"`)

	status, body, resp := postForm(t, srv, "/tokens", url.Values{"name": {"laptop"}, "group": {"work"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create = %d: %s", status, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("create response Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	plain := plainTokenRE.FindString(body)
	if plain == "" {
		t.Fatal("create response does not show the token")
	}
	wantContains(t, body, "Token created: work / laptop")
	id, ok := store.Verify(plain)
	if !ok {
		t.Fatal("created token does not authenticate")
	}

	_, body = get(t, srv.URL+"/tokens?"+fullRangeQuery)
	wantNotContains(t, body, plain, ">never<")
	wantContains(t, body, "laptop", "…"+plain[len(plain)-4:], "/?group=work", "Unauthenticated")

	status, body, _ = postForm(t, srv, "/tokens", url.Values{"name": {"  "}, "group": {"work"}}, nil)
	if status != http.StatusBadRequest {
		t.Errorf("empty name = %d, want 400", status)
	}
	wantContains(t, body, "name is required")

	idPath := "/tokens/" + strconv.FormatInt(id, 10)
	if status, _, _ := postForm(t, srv, idPath, url.Values{"name": {"laptop2"}, "group": {"home"}}, nil); status != http.StatusSeeOther {
		t.Errorf("update = %d, want 303", status)
	}
	if status, _, _ := postForm(t, srv, idPath+"/revoke", nil, nil); status != http.StatusSeeOther {
		t.Errorf("revoke = %d, want 303", status)
	}
	if _, ok := store.Verify(plain); ok {
		t.Error("revoked token still authenticates")
	}
	_, body = get(t, srv.URL+"/tokens")
	wantContains(t, body, "laptop2", "/?group=home", "(revoked ")

	status, body, resp = postForm(t, srv, idPath+"/regenerate", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("regenerate = %d: %s", status, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("regenerate Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	wantContains(t, body, "New secret for home / laptop2")
	fresh := plainTokenRE.FindString(body)
	if fresh == "" || fresh == plain {
		t.Fatal("regenerate response does not show a new secret")
	}
	if got, ok := store.Verify(fresh); !ok || got != id {
		t.Errorf("regenerated secret verify = %d, %v; want %d", got, ok, id)
	}
	if _, ok := store.Verify(plain); ok {
		t.Error("old secret still authenticates after regenerate")
	}
	if status, _, _ := postForm(t, srv, "/tokens/999/regenerate", nil, nil); status != http.StatusNotFound {
		t.Errorf("regenerate missing = %d, want 404", status)
	}

	if status, _, _ := postForm(t, srv, "/tokens/999/revoke", nil, nil); status != http.StatusNotFound {
		t.Errorf("revoke missing = %d, want 404", status)
	}
	if status, _, _ := postForm(t, srv, "/tokens/abc", url.Values{"name": {"x"}}, nil); status != http.StatusNotFound {
		t.Errorf("update bad id = %d, want 404", status)
	}
}

func TestTokensRejectCrossOrigin(t *testing.T) {
	srv, store := newTokenServer(t)
	for name, hdr := range map[string]map[string]string{
		"sec-fetch-site": {"Sec-Fetch-Site": "cross-site"},
		"origin":         {"Origin": "https://evil.example"},
	} {
		t.Run(name, func(t *testing.T) {
			status, _, _ := postForm(t, srv, "/tokens", url.Values{"name": {"x"}}, hdr)
			if status != http.StatusForbidden {
				t.Errorf("cross-origin create = %d, want 403", status)
			}
		})
	}
	if store.HasTokens() {
		t.Error("cross-origin request created a token")
	}
	status, _, _ := postForm(t, srv, "/tokens", url.Values{"name": {"x"}}, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if status != http.StatusCreated {
		t.Errorf("same-origin create = %d, want 201", status)
	}
}

func TestTokenFilterAndBreakdowns(t *testing.T) {
	srv, store := newTokenServer(t)
	_, body := get(t, srv.URL+"/?"+fullRangeQuery)
	wantNotContains(t, body, `name="token"`, `name="group"`)
	_, body = get(t, srv.URL+"/breakdowns?"+fullRangeQuery)
	wantNotContains(t, body, "By token group")

	if _, _, err := store.Create(context.Background(), "machine1", "private"); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, srv.URL+"/?"+fullRangeQuery+"&group=private")
	wantContains(t, body, `name="token"`, `name="group"`, "private / machine1", `<option value="private" selected>`)

	status, body := get(t, srv.URL+"/breakdowns?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("breakdowns = %d", status)
	}
	wantContains(t, body, "By token group", "By token", "Unauthenticated", "No group")

	// Range presets keep the token filter.
	_, body = get(t, srv.URL+"/breakdowns?token="+storage.TokenNone)
	wantContains(t, body, "/breakdowns?range=7d&amp;token=none")

	if status, _ := get(t, srv.URL+"/?token=abc"); status != http.StatusBadRequest {
		t.Errorf("invalid token filter = %d, want 400", status)
	}
}
