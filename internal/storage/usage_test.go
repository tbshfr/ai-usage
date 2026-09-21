package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

func TestInsertGenerationDeduplicates(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}

	gen := normalize.Generation{
		ID:          "abc",
		Timestamp:   time.UnixMilli(1000).UTC(),
		Source:      "copilot",
		ServiceName: "copilot-chat",
		TraceID:     "t1",
		SpanID:      "s1",
		InputTokens: ptr(int64(100)),
	}

	inserted, err := InsertGeneration(ctx, db, gen)
	if err != nil || !inserted {
		t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
	}
	inserted, err = InsertGeneration(ctx, db, gen)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("second identical insert must be deduplicated, not inserted")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1", count)
	}
}

func TestInsertGenerationMergesNullsOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}

	first := normalize.Generation{
		ID:        "abc",
		Timestamp: time.UnixMilli(1000).UTC(),
		Source:    "copilot",
		TraceID:   "t1",
		SpanID:    "s1",
		Model:     "gpt-5.6-luna",
	}
	second := normalize.Generation{
		ID:        "abc",
		Timestamp: time.UnixMilli(1000).UTC(),
		Source:    "copilot",
		TraceID:   "t1",
		SpanID:    "s1",
		// new information: previously NULL fields
		InputTokens: ptr(int64(500)),
		Cost:        ptr(0.25),
		// conflicting non-nil field must not overwrite
		Model: "claude-haiku-4-5-20251001",
	}

	if inserted, err := InsertGeneration(ctx, db, first); err != nil || !inserted {
		t.Fatalf("first: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := InsertGeneration(ctx, db, second); err != nil || inserted {
		t.Fatalf("second: inserted=%v err=%v (merge counts as dedup)", inserted, err)
	}

	var model string
	var input, output *int64
	var cost *float64
	var timestamp int64
	err = db.QueryRow(`SELECT model, input_tokens, output_tokens, cost, timestamp FROM generations WHERE id = 'abc'`).
		Scan(&model, &input, &output, &cost, &timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5.6-luna" {
		t.Errorf("model = %q, merge must not overwrite non-nil values", model)
	}
	if input == nil || *input != 500 {
		t.Errorf("input_tokens = %v, want 500 (NULL filled from new record)", input)
	}
	if output != nil {
		t.Errorf("output_tokens = %v, want NULL", output)
	}
	if cost == nil || *cost != 0.25 {
		t.Errorf("cost = %v, want 0.25", cost)
	}
	if timestamp != 1000 {
		t.Errorf("timestamp = %d, want 1000", timestamp)
	}
}

// Phase 7 item 1: partial-then-complete retries fill the final row with all
// fields; nothing is overwritten.
func TestInsertGenerationMergePartialThenComplete(t *testing.T) {
	ctx := context.Background()
	db := openedDB(t)

	partial := normalize.Generation{
		ID:           "abc",
		Timestamp:    time.UnixMilli(1000).UTC(),
		Source:       "copilot",
		TraceID:      "t1",
		SpanID:       "s1",
		OutputTokens: ptr(int64(20)),
	}
	complete := normalize.Generation{
		ID:                  "abc",
		Timestamp:           time.UnixMilli(1000).UTC(),
		Source:              "copilot",
		TraceID:             "t1",
		SpanID:              "s1",
		Provider:            "github",
		Model:               "gpt-5.6-luna",
		InputTokens:         ptr(int64(100)),
		OutputTokens:        ptr(int64(20)),
		CacheReadTokens:     ptr(int64(7)),
		CacheCreationTokens: ptr(int64(3)),
		ReasoningTokens:     ptr(int64(9)),
		ConversationID:      "conv-1",
		Duration:            1500 * time.Millisecond,
	}

	if inserted, err := InsertGeneration(ctx, db, partial); err != nil || !inserted {
		t.Fatalf("partial: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := InsertGeneration(ctx, db, complete); err != nil || inserted {
		t.Fatalf("complete retry: inserted=%v err=%v", inserted, err)
	}

	g, found, err := GenerationByID(ctx, db, "abc")
	if err != nil || !found {
		t.Fatalf("generation by id: found=%v err=%v", found, err)
	}
	if g.Provider != "github" || g.Model != "gpt-5.6-luna" {
		t.Errorf("provider/model = %q/%q, want github/gpt-5.6-luna", g.Provider, g.Model)
	}
	if g.InputTokens == nil || *g.InputTokens != 100 {
		t.Errorf("input_tokens = %v, want 100", g.InputTokens)
	}
	if g.CacheReadTokens == nil || *g.CacheReadTokens != 7 {
		t.Errorf("cache_read_tokens = %v, want 7", g.CacheReadTokens)
	}
	if g.CacheCreationTokens == nil || *g.CacheCreationTokens != 3 {
		t.Errorf("cache_creation_tokens = %v, want 3", g.CacheCreationTokens)
	}
	if g.ReasoningTokens == nil || *g.ReasoningTokens != 9 {
		t.Errorf("reasoning_tokens = %v, want 9", g.ReasoningTokens)
	}
	if g.ConversationID != "conv-1" {
		t.Errorf("conversation_id = %q, want conv-1", g.ConversationID)
	}
	if g.Duration != 1500*time.Millisecond {
		t.Errorf("duration = %v, want 1.5s", g.Duration)
	}
	if g.OutputTokens == nil || *g.OutputTokens != 20 {
		t.Errorf("output_tokens = %v, want 20 (unchanged)", g.OutputTokens)
	}
}

// Phase 7 item 1: complete first, partial retry → row unchanged.
func TestInsertGenerationCompleteFirstPartialRetry(t *testing.T) {
	ctx := context.Background()
	db := openedDB(t)

	complete := normalize.Generation{
		ID:           "abc",
		Timestamp:    time.UnixMilli(1000).UTC(),
		Source:       "opencode",
		Provider:     "anthropic",
		Model:        "claude-haiku-4-5-20251001",
		InputTokens:  ptr(int64(100)),
		OutputTokens: ptr(int64(50)),
		Cost:         ptr(0.25),
	}
	partial := normalize.Generation{
		ID:          "abc",
		Timestamp:   time.UnixMilli(2000).UTC(),
		Source:      "opencode",
		InputTokens: ptr(int64(999)),
	}

	if inserted, err := InsertGeneration(ctx, db, complete); err != nil || !inserted {
		t.Fatalf("complete: inserted=%v err=%v", inserted, err)
	}
	before, _, err := GenerationByID(ctx, db, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := InsertGeneration(ctx, db, partial); err != nil || inserted {
		t.Fatalf("partial retry: inserted=%v err=%v", inserted, err)
	}
	after, _, err := GenerationByID(ctx, db, "abc")
	if err != nil {
		t.Fatal(err)
	}

	if after.Timestamp != before.Timestamp {
		t.Errorf("timestamp changed on partial retry: %v → %v", before.Timestamp, after.Timestamp)
	}
	if after.InputTokens == nil || *after.InputTokens != 100 {
		t.Errorf("input_tokens = %v, want 100 (partial retry must not overwrite)", after.InputTokens)
	}
	if after.OutputTokens == nil || *after.OutputTokens != 50 {
		t.Errorf("output_tokens = %v, want 50", after.OutputTokens)
	}
	if after.Cost == nil || *after.Cost != 0.25 {
		t.Errorf("cost = %v, want 0.25", after.Cost)
	}
	if after.Provider != "anthropic" || after.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("provider/model = %q/%q, must be unchanged", after.Provider, after.Model)
	}
}

// A retried batch with no new information must not re-queue pricing for an
// already-enriched row; only a merge that fills model or token columns
// re-queues it, and a merge carrying a harness cost finalizes the row.
func TestInsertGenerationMergePricingQueue(t *testing.T) {
	ctx := context.Background()
	db := openedDB(t)

	gen := normalize.Generation{
		ID:           "abc",
		Timestamp:    time.UnixMilli(1000).UTC(),
		Source:       "opencode",
		Provider:     "anthropic",
		Model:        "claude-haiku-4-5",
		InputTokens:  ptr(int64(100)),
		OutputTokens: ptr(int64(20)),
	}
	if inserted, err := InsertGeneration(ctx, db, gen); err != nil || !inserted {
		t.Fatalf("insert: inserted=%v err=%v", inserted, err)
	}
	// Simulate the pricing worker's result: estimated, rates saved, queue drained.
	if _, err := db.Exec(`UPDATE generations SET cost = 1.5, cost_source = 'openrouter',
		pricing_rates = '{}', pricing_pending = 0, pricing_revision = 5 WHERE id = 'abc'`); err != nil {
		t.Fatal(err)
	}

	state := func() (pending, revision int, cost any, source string, harness int, rates any) {
		err := db.QueryRow(`SELECT pricing_pending, pricing_revision, cost, cost_source,
			cost_reported_by_harness, pricing_rates FROM generations WHERE id = 'abc'`).
			Scan(&pending, &revision, &cost, &source, &harness, &rates)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	// Retried batch, same information: the pricing state is untouched. The
	// revision still counts every merge, so enrichment racing a re-delivered
	// record cannot land stale results.
	if inserted, err := InsertGeneration(ctx, db, gen); err != nil || inserted {
		t.Fatalf("retry: inserted=%v err=%v", inserted, err)
	}
	if pending, revision, cost, _, _, rates := state(); pending != 0 || revision != 6 || cost != 1.5 || rates != "{}" {
		t.Fatalf("no-op merge re-queued pricing: pending=%d revision=%d cost=%v rates=%v", pending, revision, cost, rates)
	}

	// A retry carrying more tokens re-queues the row for re-estimation.
	more := gen
	more.CacheReadTokens = ptr(int64(30))
	if inserted, err := InsertGeneration(ctx, db, more); err != nil || inserted {
		t.Fatalf("token merge: inserted=%v err=%v", inserted, err)
	}
	if pending, revision, cost, _, _, _ := state(); pending != 1 || revision != 7 || cost != 1.5 {
		t.Fatalf("token merge must re-queue: pending=%d revision=%d cost=%v", pending, revision, cost)
	}

	// A retry carrying a harness cost finalizes the row instead.
	paid := gen
	paid.Cost = ptr(0.75)
	if inserted, err := InsertGeneration(ctx, db, paid); err != nil || inserted {
		t.Fatalf("cost merge: inserted=%v err=%v", inserted, err)
	}
	if pending, revision, cost, source, harness, rates := state(); pending != 0 || revision != 8 ||
		cost != 0.75 || source != "harness" || harness != 1 || rates != nil {
		t.Fatalf("cost merge must finalize: pending=%d revision=%d cost=%v source=%s harness=%d rates=%v",
			pending, revision, cost, source, harness, rates)
	}
}

// Phase 7 item 7: a generation with only input_tokens set keeps the other
// token columns NULL in the DB, summary/timeseries treat them as absent, and
// no phantom values appear.
func TestSparseGenerationSummaryAndTimeseries(t *testing.T) {
	ctx := context.Background()
	db := openedDB(t)

	gen := normalize.Generation{
		ID:          "sparse",
		Timestamp:   time.UnixMilli(1000).UTC(),
		Source:      "copilot",
		Provider:    "github",
		Model:       "gpt-4.1",
		InputTokens: ptr(int64(123)),
		TraceID:     "t1",
		SpanID:      "s1",
	}
	if inserted, err := InsertGeneration(ctx, db, gen); err != nil || !inserted {
		t.Fatalf("insert: inserted=%v err=%v", inserted, err)
	}

	var output, cost any
	if err := db.QueryRow(`SELECT output_tokens, cost FROM generations WHERE id = 'sparse'`).Scan(&output, &cost); err != nil {
		t.Fatal(err)
	}
	if output != nil {
		t.Errorf("output_tokens stored as %v, want NULL", output)
	}
	if cost != nil {
		t.Errorf("cost stored as %v, want NULL", cost)
	}

	f := Filter{From: time.UnixMilli(0)}
	s, err := Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.InputTokens != 123 {
		t.Errorf("summary input = %d, want 123 (tokens shown)", s.InputTokens)
	}
	if s.OutputTokens != 0 {
		t.Errorf("summary output = %d, want 0 (absent, never fabricated)", s.OutputTokens)
	}
	if s.CostKnownCount != 0 || s.CostTotal != nil {
		t.Errorf("cost known=%d total=%v, want 0/nil", s.CostKnownCount, s.CostTotal)
	}

	pts, err := Timeseries(ctx, db, f, BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("timeseries points = %d, want 1", len(pts))
	}
	if pts[0].OutputTokens != 0 || pts[0].InputTokens != 123 {
		t.Errorf("timeseries point = %+v, want input=123 output=0", pts[0])
	}
}

func openedDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

func ptr[T int64 | float64](v T) *T { return &v }
