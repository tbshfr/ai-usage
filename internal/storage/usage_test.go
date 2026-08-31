package storage

import (
	"context"
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

func ptr[T int64 | float64](v T) *T { return &v }
