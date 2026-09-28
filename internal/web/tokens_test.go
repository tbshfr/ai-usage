package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

	_, body = get(t, srv.URL+"/tokens")
	wantNotContains(t, body, plain, ">never<", `id="filter-bar"`)
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

	if _, _, err := store.Create(context.Background(), "ungrouped", ""); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, srv.URL+"/?"+fullRangeQuery)
	wantNotContains(t, body, `name="group"`)
	wantContains(t, body, `name="token"`, `name="ungrouped"`)

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
	_, body = get(t, srv.URL+"/tokens")
	wantContains(t, body, `href="/?ungrouped=true">No group</a>`)
	_, body = get(t, srv.URL+"/breakdowns?ungrouped=true")
	wantContains(t, body, `name="ungrouped" value="true" checked`, "/breakdowns?range=7d&amp;ungrouped=true")
	if status, _ := get(t, srv.URL+"/?ungrouped=invalid"); status != http.StatusBadRequest {
		t.Errorf("invalid ungrouped filter = %d, want 400", status)
	}
}

func TestTokenManagementRequiresSharedStore(t *testing.T) {
	db := seedtest.EmptyDB(t)
	listenerStore, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, id, err := listenerStore.Create(context.Background(), "client", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
	defer srv.Close()
	_, body := get(t, srv.URL+"/tokens")
	wantContains(t, body, "Token management is unavailable.")
	wantNotContains(t, body, `action="/tokens"`, `action="/tokens/1/revoke"`)
	for _, path := range []string{"/tokens", fmt.Sprintf("/tokens/%d", id), fmt.Sprintf("/tokens/%d/revoke", id), fmt.Sprintf("/tokens/%d/regenerate", id)} {
		status, _, _ := postForm(t, srv, path, url.Values{"name": {"changed"}}, nil)
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s without shared store = %d, want 503", path, status)
		}
	}
	for _, path := range []string{"/tokens/abc", "/tokens/0", "/tokens/-1", "/tokens/abc/revoke", "/tokens/abc/regenerate"} {
		status, _, _ := postForm(t, srv, path, url.Values{"name": {"changed"}}, nil)
		if status != http.StatusNotFound {
			t.Errorf("%s without shared store = %d, want 404", path, status)
		}
	}
	tokens, err := storage.ListTokens(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "client" || !tokens[0].Active() {
		t.Fatalf("management without shared store changed tokens: %+v", tokens)
	}
}

func TestUngroupedFilterAvailability(t *testing.T) {
	srv, store := newTokenServer(t)
	_, id, err := store.Create(context.Background(), "client", "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"/", "/trends", "/breakdowns", "/sessions"} {
		_, body := get(t, srv.URL+page+"?range=all")
		wantContains(t, body, `name="token"`)
		wantNotContains(t, body, `name="ungrouped"`)
		// Keep a selected filter visible so users can clear an old bookmark
		// after the last ungrouped token has moved to a group.
		_, body = get(t, srv.URL+page+"?range=all&ungrouped=true")
		wantContains(t, body, `name="ungrouped" value="true" checked`)
	}
	if status, body, _ := postForm(t, srv, fmt.Sprintf("/tokens/%d", id), url.Values{"name": {"client"}, "group": {""}}, nil); status != http.StatusSeeOther {
		t.Fatalf("remove group = %d: %s", status, body)
	}
	if err := store.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Revoked tokens still have historical usage and must remain filterable.
	_, body := get(t, srv.URL+"/?range=all")
	wantContains(t, body, `name="ungrouped"`)
}

func TestSavedTokenSecretShownWhenUsageReadFails(t *testing.T) {
	// Removing the usage table leaves token writes possible but makes the
	// subsequent page query fail. The successful response must still show
	// the only recoverable copy of the new secret.
	db := seedtest.EmptyDB(t)
	shared, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, id, err := shared.Create(context.Background(), "client", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE generations`); err != nil {
		t.Fatal(err)
	}
	h := New(db, nil, nil, nil, "test", WithTokens(shared))
	for _, path := range []string{"/tokens", fmt.Sprintf("/tokens/%d/regenerate", id)} {
		r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader("name=new-client"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		wantStatus := http.StatusCreated
		if path != "/tokens" {
			wantStatus = http.StatusOK
		}
		secret := plainTokenRE.FindString(w.Body.String())
		if w.Code != wantStatus || secret == "" || secret == plain || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("saved secret lost at %s: status %d, body %s", path, w.Code, w.Body.String())
		}
		if _, ok := shared.Verify(secret); !ok {
			t.Fatal("displayed secret does not authenticate")
		}
	}
}

func TestTokenManagementRejectsRebindingHost(t *testing.T) {
	db := seedtest.EmptyDB(t)
	store, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, id, err := store.Create(context.Background(), "existing", "")
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, nil, nil, nil, "test", WithTokens(store))
	for _, path := range []string{"/tokens", fmt.Sprintf("/tokens/%d", id), fmt.Sprintf("/tokens/%d/revoke", id), fmt.Sprintf("/tokens/%d/regenerate", id)} {
		for _, fetchSite := range []string{"", "same-origin"} {
			r := httptest.NewRequest("POST", "http://attacker.example:8080"+path, strings.NewReader("name=attacker"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "http://attacker.example:8080")
			r.Header.Set("Sec-Fetch-Site", fetchSite)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || plainTokenRE.MatchString(w.Body.String()) {
				t.Fatalf("%s with fetch-site %q: status=%d body=%s", path, fetchSite, w.Code, w.Body.String())
			}
		}
	}
	tokens, err := storage.ListTokens(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "existing" || !tokens[0].Active() {
		t.Fatalf("rejected requests changed tokens: %+v", tokens)
	}
	if _, ok := store.Verify(plain); !ok {
		t.Fatal("rejected requests invalidated original secret")
	}
}

func TestAuthenticatedTokenManagementAllowsCustomHost(t *testing.T) {
	db := seedtest.EmptyDB(t)
	store, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := mustSessions(t)
	dash := auth.NewDashboard("admin", "secret", sessions)
	h := NewAuthed(db, nil, nil, dash, nil, "test", WithTokens(store))
	issued := httptest.NewRecorder()
	sessions.Issue(issued)
	for _, loggedIn := range []bool{false, true} {
		r := httptest.NewRequest("POST", "https://dashboard.example/tokens", strings.NewReader("name=client"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://dashboard.example")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if loggedIn {
			r.AddCookie(issued.Result().Cookies()[0])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusSeeOther
		if loggedIn {
			want = http.StatusCreated
		}
		if w.Code != want {
			t.Fatalf("loggedIn=%v: status=%d body=%s", loggedIn, w.Code, w.Body.String())
		}
		if !loggedIn && store.HasTokens() {
			t.Fatal("unauthenticated request created a token")
		}
		if loggedIn {
			if _, ok := store.Verify(plainTokenRE.FindString(w.Body.String())); !ok {
				t.Fatal("created secret does not authenticate")
			}
		}
	}
}
