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
		{ID: "n1", Timestamp: day("2026-03-01"), Source: "copilot", Model: "gpt-4.1", InputTokens: seedtest.IP(5), AgentName: "titlegen"},
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

// TestSessionsXtabGroupsPerDay checks that VS Code autocomplete groups per
// UTC day under an "Autocomplete" header, even when the rows carry a
// conversation ID.
func TestSessionsXtabGroupsPerDay(t *testing.T) {
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
		{ID: "x1", Timestamp: day("2026-03-01"), Source: "copilot", Model: "copilot-nes-lysithea-14", InputTokens: seedtest.IP(5), AgentName: normalize.AgentXtabProvider, ConversationID: "conv-xtab-1"},
		{ID: "x2", Timestamp: day("2026-03-01").Add(time.Minute), Source: "copilot", Model: "copilot-nes-lysithea-14", InputTokens: seedtest.IP(5), AgentName: normalize.AgentXtabProvider, ConversationID: "conv-xtab-2"},
		{ID: "x3", Timestamp: day("2026-03-02"), Source: "copilot", Model: "copilot-nes-lysithea-14", InputTokens: seedtest.IP(5), AgentName: normalize.AgentXtabProvider, ConversationID: "conv-xtab-3"},
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
	// Two same-day Xtab rows (distinct conversations) merge into one
	// Autocomplete card per day; no per-conversation session cards surface.
	if got := strings.Count(body, "Autocomplete - "); got != 2 {
		t.Errorf("Autocomplete cards = %d, want 2", got)
	}
	wantContains(t, body, "VS Code Copilot", "Autocomplete - 2026-03-01", "Autocomplete - 2026-03-02")
	wantNotContains(t, body, "conv-xtab-1", "conv-xtab-2", "conv-xtab-3")
	// Autocomplete cards drill down to the autocomplete sentinel.
	wantContains(t, body, "conversation=autocomplete")
}

// TestSessionsTitleProgressGroupsPerDay checks that the title and
// progressMessages helpers share one per-day "Title / progress" card.
func TestSessionsTitleProgressGroupsPerDay(t *testing.T) {
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
		{ID: "t1", Timestamp: day("2026-03-01"), Source: "copilot", Model: "gpt-4o-mini-2024-07-18", InputTokens: seedtest.IP(5), AgentName: normalize.AgentTitle},
		{ID: "p1", Timestamp: day("2026-03-01").Add(time.Minute), Source: "copilot", Model: "gpt-4o-mini-2024-07-18", InputTokens: seedtest.IP(5), AgentName: normalize.AgentProgressMessages},
		{ID: "t2", Timestamp: day("2026-03-02"), Source: "copilot", Model: "gpt-4o-mini-2024-07-18", InputTokens: seedtest.IP(5), AgentName: normalize.AgentTitle},
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
	if got := strings.Count(body, "Title / progress - "); got != 2 {
		t.Errorf("Title/progress cards = %d, want 2 (same-day title+progress merge)", got)
	}
	wantContains(t, body, "VS Code Copilot", "Title / progress - 2026-03-01", "Title / progress - 2026-03-02", "conversation=titleprogress")
}

// newServerFromDB is newServer with a custom seed.
func newServerFromDB(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	return httptest.NewServer(New(db, nil, nil, nil, "test"))
}
