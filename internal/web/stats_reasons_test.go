package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func seedReasons(t *testing.T, db *sql.DB, day string) {
	t.Helper()
	if err := storage.UpsertDailyReasons(context.Background(), db, day, []storage.ReasonStat{
		{Kind: ingest.ReasonKindRejected, Reason: ingest.ReasonNoSource, Count: 4},
		{Kind: ingest.ReasonKindHTTPReject, Reason: ingest.ReasonUnauthorized, Count: 9},
		{Kind: ingest.ReasonKindDedup, Reason: "copilot", Count: 2},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsReasonsFragmentForPersistedDay(t *testing.T) {
	db := seedtest.DB(t)
	yesterday := utcDate(time.Now().AddDate(0, 0, -1))
	seedReasons(t, db, yesterday)
	srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/fragments/stats-reasons?day="+url.QueryEscape(yesterday))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	// Human labels and counts, grouped with the transport footnote.
	wantContains(t, body, "No known source", "9", "VS Code Copilot",
		"Rejected spans", "Transport rejections", "counted per request")
	wantNotContains(t, body, "no_source", "unauthorized")
}

func TestStatsReasonsFragmentLiveReplacesPersistedForToday(t *testing.T) {
	db := seedtest.DB(t)
	today := utcDate(time.Now())
	seedReasons(t, db, today)
	// Production ReasonCounts returns base + session (persisted 9 + 1 new
	// = 10); the fragment must show 10, not 9+10=19.
	srv := httptest.NewServer(New(db, nil,
		func() ingest.ReasonCounts {
			return ingest.ReasonCounts{ingest.ReasonKindHTTPReject: {ingest.ReasonUnauthorized: 10}}
		}, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/fragments/stats-reasons?day="+url.QueryEscape(today))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "10", "(live)")
	wantNotContains(t, body, "19")
}

func TestStatsReasonsFragmentCloseAndInvalidDay(t *testing.T) {
	db := seedtest.DB(t)
	srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
	defer srv.Close()

	// Missing day closes the panel with an empty body.
	status, body := get(t, srv.URL+"/fragments/stats-reasons")
	if status != http.StatusOK || body != "" {
		t.Errorf("close: status %d body %q, want 200 empty", status, body)
	}
	// A malformed day is a client error, never a lookup.
	status, _ = get(t, srv.URL+"/fragments/stats-reasons?day=not-a-day")
	if status != http.StatusBadRequest {
		t.Errorf("invalid day: status %d, want 400", status)
	}
	// A valid day without any recorded reasons renders the empty note.
	status, body = get(t, srv.URL+"/fragments/stats-reasons?day=2001-01-01")
	if status != http.StatusOK {
		t.Fatalf("empty day: status %d", status)
	}
	wantContains(t, body, "No rejections, errors, or duplicates")
}

func TestStatsPageKeepsRejectionOnlyDays(t *testing.T) {
	db := seedtest.DB(t)
	day := "2026-08-01"
	// A day whose only traffic was rejected before the pipeline: the
	// stats_daily row has zero record counters, but the reason rows exist.
	if err := storage.UpsertDailyStats(context.Background(), db, storage.DailyStats{Day: day}); err != nil {
		t.Fatal(err)
	}
	seedReasons(t, db, day)
	srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/stats")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, day, "rejection, error, and duplicate")
}

func TestReasonLabelsCoverHTTPRejectReasons(t *testing.T) {
	// Every transport-rejection reason needs a friendly label; a new
	// HTTPRejectReasons entry without a reasonLabel case would render raw.
	for _, reason := range ingest.HTTPRejectReasons {
		if got := reasonLabel(ingest.ReasonKindHTTPReject, reason); got == reason {
			t.Errorf("reasonLabel(http_reject, %q) = raw %q, want friendly label", reason, got)
		}
	}
}

func TestReasonLabelsCoverRecordReasons(t *testing.T) {
	// Same guard for the record-level reasons (rejected/ignored/norm_error
	// kinds): a new enum entry without a reasonLabel case would render raw.
	reasons := []string{
		ingest.ReasonNoSource,
		ingest.ReasonNotGeneration,
		ingest.ReasonLogs,
		ingest.ReasonMetrics,
		ingest.ReasonBadAttrs,
		ingest.ReasonBadIDs,
		ingest.ReasonBadTimestamp,
		ingest.ReasonNormOther,
	}
	for _, reason := range reasons {
		for _, kind := range []string{ingest.ReasonKindRejected, ingest.ReasonKindIgnored, ingest.ReasonKindNormError} {
			if got := reasonLabel(kind, reason); got == reason {
				t.Errorf("reasonLabel(%s, %q) = raw %q, want friendly label", kind, reason, got)
			}
		}
	}
}

func TestStatsReasonsFragmentRendersUnknownKind(t *testing.T) {
	db := seedtest.DB(t)
	day := "2026-08-02"
	if err := storage.UpsertDailyReasons(context.Background(), db, day, []storage.ReasonStat{
		{Kind: ingest.ReasonKindRejected, Reason: ingest.ReasonNoSource, Count: 1},
		{Kind: "future_kind", Reason: "future_reason", Count: 2},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(db, nil, nil, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/fragments/stats-reasons?day="+url.QueryEscape(day))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	// Unknown kinds render with their raw kind as fallback label instead
	// of being dropped (parity with the JSON API, which sorts them last).
	wantContains(t, body, "future_kind", "future_reason")
}
