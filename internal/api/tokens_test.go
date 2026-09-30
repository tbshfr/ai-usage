package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestTokenEndpoints(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	id, err := storage.CreateToken(ctx, db, "laptop", "work", sha256.Sum256([]byte("secret-token-value")), "alue")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{
		{ID: "a", Timestamp: ts, Source: "opencode", InputTokens: seedtest.IP(10), TokenID: id},
		{ID: "b", Timestamp: ts, Source: "opencode", InputTokens: seedtest.IP(5)},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(db, testLogger(t), nil, nil, nil, "test"))
	defer srv.Close()
	const rng = "from=2026-01-01&to=2026-04-01"

	status, body := get(t, srv.URL+"/api/tokens")
	if status != http.StatusOK {
		t.Fatalf("/api/tokens = %d", status)
	}
	if strings.Contains(body, "secret-token-value") || strings.Contains(body, "hash") {
		t.Errorf("/api/tokens leaks secrets: %s", body)
	}
	var tokens []tokenInfo
	if err := json.Unmarshal([]byte(body), &tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID != id || tokens[0].Group != "work" || !tokens[0].Active || tokens[0].Hint != "alue" {
		t.Errorf("/api/tokens = %+v", tokens)
	}

	var sum summaryResponse
	_, body = get(t, srv.URL+"/api/summary?"+rng+"&group=work")
	if err := json.Unmarshal([]byte(body), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.InputTokens != 10 || sum.Filter.Group != "work" {
		t.Errorf("summary group=work: input %d filter %+v", sum.InputTokens, sum.Filter)
	}
	_, body = get(t, srv.URL+"/api/summary?"+rng+"&token=none")
	if err := json.Unmarshal([]byte(body), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.InputTokens != 5 {
		t.Errorf("summary token=none: input %d, want 5", sum.InputTokens)
	}
	if status, _ := get(t, srv.URL+"/api/summary?token=x"); status != http.StatusBadRequest {
		t.Errorf("invalid token filter = %d, want 400", status)
	}

	var rows []breakdownRow
	_, body = get(t, srv.URL+"/api/tokens/usage?"+rng)
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != strconv.FormatInt(id, 10) || rows[1].Key != "" {
		t.Errorf("/api/tokens/usage = %+v", rows)
	}
	_, body = get(t, srv.URL+"/api/groups?"+rng)
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "work" {
		t.Errorf("/api/groups = %+v", rows)
	}

	var gens []generation
	_, body = get(t, srv.URL+"/api/generations?"+rng+"&token="+strconv.FormatInt(id, 10))
	if err := json.Unmarshal([]byte(body), &gens); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(gens) != 1 || gens[0].TokenID == nil || *gens[0].TokenID != id {
		t.Errorf("/api/generations token filter = %+v", gens)
	}
	ungrouped, err := storage.CreateToken(ctx, db, "ungrouped", "", sha256.Sum256([]byte("ungrouped")), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.InsertGenerations(ctx, db, []normalize.Generation{
		{ID: "c", Timestamp: ts, Source: "opencode", InputTokens: seedtest.IP(3), TokenID: ungrouped},
	}); err != nil {
		t.Fatal(err)
	}
	status, body = get(t, srv.URL+"/api/summary?"+rng+"&ungrouped=true")
	if err := json.Unmarshal([]byte(body), &sum); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || sum.Requests != 1 || sum.InputTokens != 3 || !sum.Filter.Ungrouped {
		t.Errorf("summary ungrouped=true = %d, %+v", status, sum)
	}
	if status, _ := get(t, srv.URL+"/api/summary?ungrouped=invalid"); status != http.StatusBadRequest {
		t.Errorf("invalid ungrouped filter = %d, want 400", status)
	}
}

func TestNewWithSharedTokens(t *testing.T) {
	db := seedtest.EmptyDB(t)
	store, err := auth.NewTokenStore(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, testLogger(t), nil, nil, nil, "test", WithTokens(store))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://localhost/settings/tokens", strings.NewReader("name=client"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create through api.New = %d: %s", w.Code, w.Body.String())
	}
	tokens, err := storage.ListTokens(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "client" || store.ActiveCount() != 1 || !store.HasTokens() {
		t.Fatalf("shared store was not updated: tokens=%+v active=%d", tokens, store.ActiveCount())
	}
}
