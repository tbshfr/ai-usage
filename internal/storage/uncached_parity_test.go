package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// TestUncachedInputSQLParity feeds fixed rows through both implementations of
// the uncached-input rule: the SQL expression used by every aggregate and the
// Go mirror (normalize.Generation.UncachedInput) used for single records. If
// they drift, single-record views and aggregates disagree.
func TestUncachedInputSQLParity(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}

	i64 := func(v int64) *int64 { return &v }
	ts := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	rows := []normalize.Generation{
		// copilot, cache below prompt: subtract cache.
		{ID: "p1", Timestamp: ts, Source: normalize.SourceCopilot,
			InputTokens: i64(1000), CacheReadTokens: i64(800), CacheCreationTokens: i64(100)},
		// copilot, cache exceeds prompt: clamp to 0 in both.
		{ID: "p2", Timestamp: ts, Source: normalize.SourceCopilot,
			InputTokens: i64(300), CacheReadTokens: i64(400)},
		// copilot, nil cache fields: treated as 0 in both.
		{ID: "p3", Timestamp: ts, Source: normalize.SourceCopilot, InputTokens: i64(50)},
		// opencode, prompt already excludes cache: passthrough despite cache.
		{ID: "p4", Timestamp: ts, Source: normalize.SourceOpenCode,
			InputTokens: i64(136), CacheReadTokens: i64(6912), CacheCreationTokens: i64(5)},
		// nil input: Go returns nil; SQL counts it as 0 in aggregates.
		{ID: "p5", Timestamp: ts, Source: normalize.SourceCopilot, OutputTokens: i64(10)},
		{ID: "p6", Timestamp: ts, Source: normalize.SourceOpenCode, InputTokens: i64(7)},
	}
	for _, g := range rows {
		if _, err := InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}

	rs, err := db.QueryContext(ctx,
		`SELECT id, source, input_tokens, cache_read_tokens, cache_creation_tokens,
		`+uncachedInputSQL+` FROM generations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()

	for rs.Next() {
		var id, source string
		var input, cacheRead, cacheCreate, gotSQL sql.NullInt64
		if err := rs.Scan(&id, &source, &input, &cacheRead, &cacheCreate, &gotSQL); err != nil {
			t.Fatal(err)
		}
		g := normalize.Generation{
			ID:                  id,
			Source:              source,
			InputTokens:         nullInt64(input),
			CacheReadTokens:     nullInt64(cacheRead),
			CacheCreationTokens: nullInt64(cacheCreate),
		}
		want := g.UncachedInput()
		switch {
		case want == nil && gotSQL.Int64 != 0:
			t.Errorf("%s: SQL uncached input = %v, want 0 (unreported input counts as 0)", id, gotSQL.Int64)
		case want != nil && (!gotSQL.Valid || gotSQL.Int64 != *want):
			t.Errorf("%s: SQL uncached input = %v, want %d (Go mirror)", id, gotSQL.Int64, *want)
		}
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
}
