package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

// seedDailyStats writes yesterday's and today's persisted counters with
// values distinctive enough to assert on in the rendered table.
func seedDailyStats(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	rows := []storage.DailyStats{
		{Day: utcDate(time.Now().AddDate(0, 0, -1)), Received: 421, Stored: 400, Rejected: 12},
		{Day: utcDate(time.Now()), Received: 777, Normalized: 700},
	}
	for _, s := range rows {
		if err := storage.UpsertDailyStats(ctx, db, s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatsPageShowsTodayWithoutStatsFunc(t *testing.T) {
	db := seedtest.DB(t)
	seedDailyStats(t, db)
	// A nil stats func is documented as valid; today's persisted row is
	// then the best available data and must not be dropped.
	srv := httptest.NewServer(New(db, nil, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/stats")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "777", "700", "421", "Total (2 days)")
	wantNotContains(t, body, "(live)")
}

func TestStatsPageLiveRowReplacesPersistedToday(t *testing.T) {
	db := seedtest.DB(t)
	seedDailyStats(t, db)
	srv := httptest.NewServer(New(db, func() ingest.Stats {
		return ingest.Stats{Received: 888, Stored: 880}
	}, nil, "test"))
	defer srv.Close()

	status, body := get(t, srv.URL+"/stats")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "888", "421", "Total (2 days)")
	wantContains(t, body, "(live)")
	// the persisted snapshot for today must be replaced, not merged
	wantNotContains(t, body, "777")
}
