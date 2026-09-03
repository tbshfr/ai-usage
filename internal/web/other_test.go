package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

// TestSessionsOtherGroupsPerDay checks that requests without a conversation
// ID appear as per-UTC-day "Other" cards on the sessions page.
func TestSessionsOtherGroupsPerDay(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d.UTC()
	}
	rows := []normalize.Generation{
		{ID: "n1", Timestamp: day("2026-03-01"), Source: "copilot", Model: "gpt-4.1", InputTokens: seedtest.IP(5), AgentName: "title"},
		{ID: "n2", Timestamp: day("2026-03-01").Add(time.Minute), Source: "copilot", Model: "gpt-4.1", InputTokens: seedtest.IP(5)},
		{ID: "n3", Timestamp: day("2026-03-02"), Source: "copilot", Model: "gpt-4.1", InputTokens: seedtest.IP(5)},
	}
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServerFromDB(t, db)
	defer srv.Close()
	status, body := get(t, srv.URL+"/sessions?"+fullRangeQuery)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, "Other requests", "no conversation", "2026-03-01", "2026-03-02")
	// two same-day rows merge into one group: exactly two Other cards
	if got := strings.Count(body, "Other requests"); got != 2 {
		t.Errorf("Other cards = %d, want 2", got)
	}
}

// newServerFromDB is newServer with a custom seed.
func newServerFromDB(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	return httptest.NewServer(New(db, nil, nil, nil, "test"))
}
