package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestStatsReasonsEndpoint(t *testing.T) {
	db := seedtest.EmptyDB(t)
	if err := storage.UpsertDailyReasons(context.Background(), db, "2026-08-01", []storage.ReasonStat{
		{Kind: "http_reject", Reason: "unauthorized", Count: 3},
		{Kind: "rejected", Reason: "no_source", Count: 5},
	}); err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, db, nil)

	status, body := get(t, srv.URL+"/api/stats/reasons?day=2026-08-01")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	for _, want := range []string{`"kind":"http_reject"`, `"reason":"unauthorized"`, `"count":3`, `"kind":"rejected"`, `"reason":"no_source"`, `"count":5`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}

	// A day with nothing recorded returns an empty array, not 404.
	status, body = get(t, srv.URL+"/api/stats/reasons?day=2026-08-02")
	if status != http.StatusOK || body != "[]\n" {
		t.Errorf("missing day: status %d body %q, want 200 []", status, body)
	}

	// Bad params are client errors.
	for _, day := range []string{"", "not-a-day", "08/01/2026"} {
		status, _ = get(t, srv.URL+"/api/stats/reasons?day="+day)
		if status != http.StatusBadRequest {
			t.Errorf("day %q: status = %d, want 400", day, status)
		}
	}
}
