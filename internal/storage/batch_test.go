package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

func batchGen(id string) normalize.Generation {
	return normalize.Generation{
		ID:          id,
		Timestamp:   time.UnixMilli(1000).UTC(),
		Source:      "copilot",
		ServiceName: "copilot-chat",
		TraceID:     "t-" + id,
		SpanID:      "s-" + id,
		InputTokens: ptr(int64(100)),
	}
}

// TestInsertGenerationsBatch: one transaction stores the fresh records,
// dedups duplicates inside the batch and against existing rows, and merges
// conflicts exactly like the single-record path.
func TestInsertGenerationsBatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, err := InsertGeneration(ctx, db, batchGen("existing")); err != nil {
		t.Fatal(err)
	}

	stored, err := InsertGenerations(ctx, db, []normalize.Generation{
		batchGen("new-1"),
		batchGen("new-1"), // duplicate inside the batch
		batchGen("existing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Errorf("stored = %d, want 1 (one fresh, two deduplicated)", stored)
	}

	// A batch of conflicts merges without reporting inserts.
	merge := batchGen("existing")
	merge.Model = "gpt-5.6-luna"
	stored, err = InsertGenerations(ctx, db, []normalize.Generation{merge})
	if err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("stored = %d, want 0 (conflict merges, not inserts)", stored)
	}
	var model sql.NullString
	if err := db.QueryRow(`SELECT model FROM generations WHERE id = 'existing'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if !model.Valid || model.String != "gpt-5.6-luna" {
		t.Errorf("merged model = %v, want gpt-5.6-luna (NULL filled from the new record)", model)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("rows = %d, want 2", count)
	}
}

func TestInsertGenerationsEmptyBatch(t *testing.T) {
	stored, err := InsertGenerations(context.Background(), openTestDB(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("stored = %d, want 0 for an empty batch", stored)
	}
}

// TestConcurrentReadsAndWrites: the pool serves parallel dashboard page
// loads (aggregates + distinct lists) beside ingest writes. Collisions on
// the SQLite write lock must resolve via busy_timeout, never surface as
// errors.
func TestConcurrentReadsAndWrites(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	const writes = 60
	const readers = 4

	var wg sync.WaitGroup
	errs := make(chan error, writes+readers)

	// Readers spin until the writer finishes; every collision with the
	// write lock must be absorbed by busy_timeout.
	writerDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(writerDone)
		for i := 0; i < writes; i++ {
			gen := batchGen(fmt.Sprintf("w%d", i))
			gen.Timestamp = time.UnixMilli(int64(1000 + i)).UTC()
			if _, err := InsertGeneration(ctx, db, gen); err != nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
				return
			}
		}
	}()

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-writerDone:
					return
				default:
				}
				if _, err := Summary(ctx, db, Filter{}); err != nil {
					errs <- fmt.Errorf("summary: %w", err)
					return
				}
				if _, err := DistinctModels(ctx, db, Filter{}); err != nil {
					errs <- fmt.Errorf("distinct: %w", err)
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case err := <-errs:
		// No close(errs) here: if a second goroutine fails before
		// wg.Wait returns, its send would hit a closed channel and
		// panic instead of surfacing the error.
		wg.Wait()
		t.Fatalf("concurrent access failed: %v", err)
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
