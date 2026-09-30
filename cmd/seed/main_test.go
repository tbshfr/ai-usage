package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

func TestSeed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "demo.db")
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	const requests = 12345
	n, err := seed(ctx, path, 30, requests, now, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if n != requests {
		t.Fatalf("seed reported %d generations, want %d", n, requests)
	}

	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows, future, days int
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(CASE WHEN timestamp > ? THEN 1 END), COUNT(DISTINCT timestamp / 86400000) FROM generations`,
		now.UnixMilli()).Scan(&rows, &future, &days); err != nil {
		t.Fatal(err)
	}
	if rows != requests || future != 0 {
		t.Fatalf("generations = %d (%d in the future), want %d and none in the future", rows, future, requests)
	}
	if days < 20 {
		t.Fatalf("generations span %d days, want most of the 31", days)
	}
	tokens, err := storage.ListTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 4 {
		t.Fatalf("tokens = %d, want 4", len(tokens))
	}
	stats, err := storage.DailyStatsRange(ctx, db, "2026-08-31", "2026-09-30", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 31 {
		t.Fatalf("daily stats = %d, want 31", len(stats))
	}

	if _, err := seed(ctx, path, 30, requests, now, io.Discard); err == nil {
		t.Fatal("seeding an existing database succeeded")
	}
}

func TestSeedEdgeCounts(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name           string
		days, requests int
		now            time.Time
	}{
		{"no requests", 30, 0, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		{"one request", 30, 1, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		{"today only just started", 1, 500, time.Date(2026, 9, 30, 0, 0, 1, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "demo.db")
			n, err := seed(ctx, path, tc.days, tc.requests, tc.now, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.requests {
				t.Fatalf("seed reported %d generations, want %d", n, tc.requests)
			}
		})
	}
}
