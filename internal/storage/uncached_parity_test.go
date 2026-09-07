package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// TestCanonicalTokenSQLParity feeds fixed rows through the SQL expressions
// used by aggregates and their Go mirrors used for individual records.
func TestCanonicalTokenSQLParity(t *testing.T) {
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
		// Codex includes cache in input and reasoning in output.
		{ID: "p7", Timestamp: ts, Source: normalize.SourceCodex,
			InputTokens: i64(1000), OutputTokens: i64(132), CacheReadTokens: i64(800),
			CacheCreationTokens: i64(100), ReasoningTokens: i64(19)},
		// Defensive output clamp for inconsistent producer data.
		{ID: "p8", Timestamp: ts, Source: normalize.SourceCodex,
			OutputTokens: i64(10), ReasoningTokens: i64(20)},
	}
	for _, g := range rows {
		if _, err := InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}

	rs, err := db.QueryContext(ctx,
		`SELECT id, source, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		reasoning_tokens, `+uncachedInputSQL+`, `+outputTokensSQL+` FROM generations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()

	for rs.Next() {
		var id, source string
		var input, output, cacheRead, cacheCreate, reasoning, gotInputSQL, gotOutputSQL sql.NullInt64
		if err := rs.Scan(&id, &source, &input, &output, &cacheRead, &cacheCreate, &reasoning, &gotInputSQL, &gotOutputSQL); err != nil {
			t.Fatal(err)
		}
		g := normalize.Generation{
			ID:                  id,
			Source:              source,
			InputTokens:         nullInt64(input),
			OutputTokens:        nullInt64(output),
			CacheReadTokens:     nullInt64(cacheRead),
			CacheCreationTokens: nullInt64(cacheCreate),
			ReasoningTokens:     nullInt64(reasoning),
		}
		wantInput := g.UncachedInput()
		switch {
		case wantInput == nil && gotInputSQL.Int64 != 0:
			t.Errorf("%s: SQL uncached input = %v, want 0 (unreported input counts as 0)", id, gotInputSQL.Int64)
		case wantInput != nil && (!gotInputSQL.Valid || gotInputSQL.Int64 != *wantInput):
			t.Errorf("%s: SQL uncached input = %v, want %d (Go mirror)", id, gotInputSQL.Int64, *wantInput)
		}
		wantOutput := g.NonReasoningOutput()
		switch {
		case wantOutput == nil && gotOutputSQL.Int64 != 0:
			t.Errorf("%s: SQL output = %v, want 0 (unreported output counts as 0)", id, gotOutputSQL.Int64)
		case wantOutput != nil && (!gotOutputSQL.Valid || gotOutputSQL.Int64 != *wantOutput):
			t.Errorf("%s: SQL output = %v, want %d (Go mirror)", id, gotOutputSQL.Int64, *wantOutput)
		}
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
}
