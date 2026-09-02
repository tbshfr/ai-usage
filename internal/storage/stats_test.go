package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openStatsDB(t *testing.T) *sql.DB {
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

func upsertDay(t *testing.T, ctx context.Context, db *sql.DB, day string, received int64) {
	t.Helper()
	if err := UpsertDailyStats(ctx, db, DailyStats{Day: day, Received: received}); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertDailyStatsIsAbsoluteSnapshot(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	upsertDay(t, ctx, db, "2026-08-01", 10)
	upsertDay(t, ctx, db, "2026-08-01", 25)

	row, found, err := DailyStatsForDay(ctx, db, "2026-08-01")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if row.Received != 25 {
		t.Errorf("received = %d, want 25 (absolute overwrite, not add)", row.Received)
	}
	if row.UpdatedAt.IsZero() {
		t.Error("updated_at must be set")
	}
}

func TestDailyStatsForDayNotFound(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	_, found, err := DailyStatsForDay(ctx, db, "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("found = true for missing day")
	}
}

func TestDailyStatsRange(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	upsertDay(t, ctx, db, "2026-08-01", 1)
	upsertDay(t, ctx, db, "2026-08-02", 2)
	upsertDay(t, ctx, db, "2026-08-03", 3)
	upsertDay(t, ctx, db, "2026-08-05", 5)

	tests := []struct {
		name  string
		from  string
		to    string
		limit int
		want  []string
	}{
		{"both bounds", "2026-08-01", "2026-08-03", 30, []string{"2026-08-03", "2026-08-02", "2026-08-01"}},
		{"from only is unbounded above", "2026-08-02", "", 30, []string{"2026-08-05", "2026-08-03", "2026-08-02"}},
		{"to only is unbounded below", "", "2026-08-02", 30, []string{"2026-08-02", "2026-08-01"}},
		{"no bounds returns all", "", "", 30, []string{"2026-08-05", "2026-08-03", "2026-08-02", "2026-08-01"}},
		{"gaps are skipped", "2026-08-03", "2026-08-04", 30, []string{"2026-08-03"}},
		{"limit keeps newest", "2026-08-01", "2026-08-05", 2, []string{"2026-08-05", "2026-08-03"}},
		{"no match", "2026-07-01", "2026-07-31", 30, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := DailyStatsRange(ctx, db, tt.from, tt.to, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.Day)
			}
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("days = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("days = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestRecentDailyStats(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	upsertDay(t, ctx, db, "2026-08-01", 1)
	upsertDay(t, ctx, db, "2026-08-02", 2)
	upsertDay(t, ctx, db, "2026-08-03", 3)

	rows, err := RecentDailyStats(ctx, db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Day != "2026-08-03" || rows[1].Day != "2026-08-02" {
		t.Fatalf("days = %v, want [2026-08-03 2026-08-02]", rows)
	}
}
