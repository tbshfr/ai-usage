// Package seedtest provides the shared Phase 4 seed fixture for tests that
// need a populated generations table (storage, API, and UI layers).
package seedtest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

// Rows returns a fixed mix of ~20 generations (computed by hand):
//   - copilot rows (cost NULL, various models, one legacy-reasoning row)
//   - opencode rows (cost set, cache_creation set)
//   - a multi-round trace (c8/c9 share a trace id)
//   - one sparse row (only input tokens: c4) and one token-less row (c11)
//   - rows across a month boundary (jan31/feb01, feb28/mar01) and a leap day
//
// DB re-inserts o1 to cover the dedup path.
func Rows(t *testing.T) []normalize.Generation {
	t.Helper()
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d.UTC()
	}
	type spec struct {
		id, source, provider, model, day string
		in, out, cacheRead, cacheCreate  *int64
		reasoning                        *int64
		cost                             *float64
	}
	specs := []spec{
		{id: "c1", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-01-31", in: IP(100), out: IP(50)},
		{id: "c2", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-01-31", in: IP(200), out: IP(100), reasoning: IP(30)},
		{id: "c3", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-01", in: IP(150), out: IP(75)},
		{id: "c4", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-02-01", in: IP(10)},
		{id: "c5", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-28", in: IP(300), out: IP(200), cacheRead: IP(400)},
		{id: "c6", source: "copilot", provider: "github", model: "claude-sonnet-4-5", day: "2026-03-01", in: IP(50), out: IP(25), reasoning: IP(5)},
		{id: "c7", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: IP(20), out: IP(10)},
		{id: "c8", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: IP(20), out: IP(10)},
		{id: "c9", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: IP(5), out: IP(5)},
		{id: "c10", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2024-02-29", in: IP(1000), out: IP(500)},
		{id: "o1", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-01-31", in: IP(10), out: IP(20), cacheCreate: IP(5), cost: FP(0.10)},
		{id: "o2", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-01", in: IP(11), out: IP(22), cacheCreate: IP(6), cost: FP(0.20)},
		{id: "o3", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-01", in: IP(12), out: IP(24), cacheCreate: IP(7), cost: FP(0.30)},
		{id: "o4", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-28", in: IP(13), out: IP(26), cacheCreate: IP(8), cost: FP(0.40)},
		{id: "o5", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-03-01", in: IP(14), out: IP(28), cacheCreate: IP(9), cost: FP(0.50)},
		{id: "o6", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-03-02", in: IP(15), out: IP(30), cacheCreate: IP(10), cost: FP(0.60)},
		{id: "o7", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2024-02-29", in: IP(16), out: IP(32), cacheCreate: IP(11), cost: FP(0.70)},
		{id: "c11", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-28"},
		{id: "o8", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-01-31", in: IP(1), out: IP(2), cost: FP(0.05)},
		{id: "c12", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-03-01", in: IP(40), out: IP(80), cacheRead: IP(10)},
	}
	out := make([]normalize.Generation, 0, len(specs))
	for i, s := range specs {
		traceID := "trace-" + s.id
		if s.id == "c8" || s.id == "c9" {
			traceID = "trace-multi-round"
		}
		out = append(out, normalize.Generation{
			ID:                  s.id,
			Timestamp:           day(s.day).Add(time.Duration(i) * time.Minute),
			Source:              s.source,
			ServiceName:         s.source,
			Provider:            s.provider,
			Model:               s.model,
			InputTokens:         s.in,
			OutputTokens:        s.out,
			CacheReadTokens:     s.cacheRead,
			CacheCreationTokens: s.cacheCreate,
			ReasoningTokens:     s.reasoning,
			Cost:                s.cost,
			TraceID:             traceID,
			SpanID:              "span-" + s.id,
			ConversationID:      "conv-" + s.source,
		})
	}
	return out
}

// DB opens, migrates, and seeds a throwaway database with Rows.
func DB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	rows := Rows(t)
	for _, g := range rows {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	// dedup case: retried batch must not add a row
	inserted, err := storage.InsertGeneration(ctx, db, rows[10])
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("re-inserting o1 must be deduplicated")
	}
	return db
}

// EmptyDB opens and migrates a throwaway database without seeding it.
func EmptyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

// FullRange covers every seeded row, including the 2024 leap day.
func FullRange() storage.Filter {
	return storage.Filter{
		From: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
}

func IP(v int64) *int64     { return &v }
func FP(v float64) *float64 { return &v }
