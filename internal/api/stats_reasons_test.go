package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/ingest"
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

func TestStatsReasonsEndpointLiveForToday(t *testing.T) {
	db := seedtest.EmptyDB(t)
	today := time.Now().UTC().Format("2006-01-02")
	if err := storage.UpsertDailyReasons(context.Background(), db, today, []storage.ReasonStat{
		{Kind: "http_reject", Reason: "unauthorized", Count: 9},
	}); err != nil {
		t.Fatal(err)
	}
	// Live (base 9 + 1 new = 10) replaces persisted 9, not adds to 19.
	srv := httptest.NewServer(New(db, testLogger(t), nil, func() ingest.ReasonCounts {
		return ingest.ReasonCounts{"http_reject": {"unauthorized": 10}}
	}, nil, "test"))
	t.Cleanup(srv.Close)

	status, body := get(t, srv.URL+"/api/stats/reasons?day="+today)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	if !strings.Contains(body, `"count":10`) {
		t.Errorf("body missing live count 10: %s", body)
	}
	if strings.Contains(body, `"count":19`) {
		t.Errorf("body double-counts persisted + live: %s", body)
	}
}

func TestStatsReasonsEndpointSkipsZeroCounts(t *testing.T) {
	db := seedtest.EmptyDB(t)
	// Zero rows (e.g. manual inserts) are hidden, matching the live path
	// and the dashboard, which both skip n == 0.
	if err := storage.UpsertDailyReasons(context.Background(), db, "2026-08-03", []storage.ReasonStat{
		{Kind: "rejected", Reason: "no_source", Count: 5},
		{Kind: "rejected", Reason: "not_a_generation", Count: 0},
	}); err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, db, nil)

	status, body := get(t, srv.URL+"/api/stats/reasons?day=2026-08-03")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	if !strings.Contains(body, `"reason":"no_source"`) {
		t.Errorf("body missing non-zero row: %s", body)
	}
	if strings.Contains(body, "not_a_generation") {
		t.Errorf("body includes zero-count row: %s", body)
	}
}
