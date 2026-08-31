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

// serverOnDB serves the dashboard against an existing database (unlike
// newServer, which seeds its own).
func serverOnDB(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(db, "test"))
	t.Cleanup(srv.Close)
	return srv
}

// Phase 7 item 7: unknown models fall back to the raw model/provider in the
// UI; cost renders as never-reported; tokens shown.
func TestDetailRendersUnknownModel(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	gen := normalize.Generation{
		ID:          "unknown-model",
		Timestamp:   time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Source:      "copilot",
		ServiceName: "copilot-chat",
		Provider:    "acme-cloud",
		Model:       "mystery-model-9000",
		InputTokens: ptr64(123),
		TraceID:     "t1",
		SpanID:      "s1",
	}
	if inserted, err := storage.InsertGeneration(ctx, db, gen); err != nil || !inserted {
		t.Fatalf("insert: inserted=%v err=%v", inserted, err)
	}

	srv := serverOnDB(t, db)
	status, body := get(t, srv.URL+"/generations/unknown-model")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body,
		"mystery-model-9000", // raw model kept, no rewrite
		"acme-cloud",         // raw provider kept
		"123",                // input tokens shown
		"Not reported by this source",
	)
	wantNotContains(t, body, "anthropic", "openai", "$")

	// breakdown table shows the unknown underlying provider
	status, body = get(t, srv.URL+"/breakdowns")
	if status != http.StatusOK {
		t.Fatalf("breakdowns status %d", status)
	}
	wantContains(t, body, "mystery-model-9000", "unknown", "acme-cloud")
}

// Phase 7 item 7: a generation with only input_tokens set renders the other
// token columns as "—" in the detail view.
func TestDetailRendersSparseTokens(t *testing.T) {
	ctx := context.Background()
	db := seedtest.EmptyDB(t)
	gen := normalize.Generation{
		ID:          "sparse",
		Timestamp:   time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Source:      "opencode",
		ServiceName: "opencode",
		Provider:    "anthropic",
		Model:       "claude-haiku-4-5-20251001",
		InputTokens: ptr64(10),
		TraceID:     "t1",
		SpanID:      "s1",
	}
	if inserted, err := storage.InsertGeneration(ctx, db, gen); err != nil || !inserted {
		t.Fatalf("insert: inserted=%v err=%v", inserted, err)
	}

	srv := serverOnDB(t, db)
	status, body := get(t, srv.URL+"/generations/sparse")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	wantContains(t, body, ">10<", "Not reported by this source")
	for _, absent := range []string{"Output tokens</dt><dd>0", "Reasoning tokens</dt><dd>0"} {
		if strings.Contains(body, absent) {
			t.Errorf("sparse detail renders %q as 0; want em dash", absent)
		}
	}
}

func ptr64(v int64) *int64 { return &v }
