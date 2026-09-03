package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

func newStatsPipelineDB(t *testing.T) *sql.DB {
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

func newStatsPipelineOn(t *testing.T, db *sql.DB) *Pipeline {
	t.Helper()
	return NewPipeline(db, nil, nil)
}

func newStatsPipeline(t *testing.T) *Pipeline {
	t.Helper()
	return newStatsPipelineOn(t, newStatsPipelineDB(t))
}

func TestSaveColdStartPersistsToToday(t *testing.T) {
	p := newStatsPipeline(t)
	p.received.Add(7)
	p.stored.Add(3)

	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	today := utcDay(time.Now())
	row, found, err := storage.DailyStatsForDay(context.Background(), p.db, today)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if row.Received != 7 || row.Stored != 3 {
		t.Errorf("received=%d stored=%d, want 7/3", row.Received, row.Stored)
	}
	if p.baseDay != today {
		t.Errorf("baseDay = %q, want %q", p.baseDay, today)
	}
}

func TestSaveRolloverWritesOldDayAndResets(t *testing.T) {
	p := newStatsPipeline(t)
	// Simulate a session that started "yesterday" with a persisted base.
	p.baseDay = "2026-08-31"
	p.base = Stats{Received: 100, Stored: 50}
	p.received.Add(5)
	p.stored.Add(2)

	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	today := utcDay(time.Now())

	// Old day holds base + session counters.
	old, found, err := storage.DailyStatsForDay(ctx, p.db, "2026-08-31")
	if err != nil || !found {
		t.Fatalf("old day found=%v err=%v", found, err)
	}
	if old.Received != 105 || old.Stored != 52 {
		t.Errorf("old day received=%d stored=%d, want 105/52", old.Received, old.Stored)
	}

	// Session counters restarted; baseDay moved to today; nothing
	// persisted for the new day yet.
	if got := p.atomicStats(); got.Received != 0 || got.Stored != 0 {
		t.Errorf("counters not reset: %+v", got)
	}
	if p.baseDay != today {
		t.Errorf("baseDay = %q, want %q", p.baseDay, today)
	}
	if _, found, err := storage.DailyStatsForDay(ctx, p.db, today); err != nil || found {
		t.Errorf("new day row exists after rollover save: found=%v err=%v", found, err)
	}

	// A second save in the new day persists today-only counters: no
	// double counting of the old day's totals.
	p.received.Add(3)
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	row, found, err := storage.DailyStatsForDay(ctx, p.db, today)
	if err != nil || !found {
		t.Fatalf("today found=%v err=%v", found, err)
	}
	if row.Received != 3 {
		t.Errorf("today received = %d, want 3", row.Received)
	}
	again, _, err := storage.DailyStatsForDay(ctx, p.db, "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if again.Received != 105 {
		t.Errorf("old day changed after second save: %d, want 105", again.Received)
	}
}

func TestRestoreBaseAndStatsContinue(t *testing.T) {
	ctx := context.Background()
	p := newStatsPipeline(t)
	today := utcDay(time.Now())

	// No row for today: base stays zero and Stats shows session only.
	if err := p.RestoreBase(ctx); err != nil {
		t.Fatal(err)
	}
	p.received.Add(4)
	if got := p.Stats(); got.Received != 4 {
		t.Errorf("stats without base = %d, want 4", got.Received)
	}

	// Persisted row for today becomes the base the session adds to.
	if err := storage.UpsertDailyStats(ctx, p.db, storage.DailyStats{
		Day: today, Received: 90, Stored: 40,
	}); err != nil {
		t.Fatal(err)
	}
	p2 := newStatsPipelineOn(t, p.db)
	if err := p2.RestoreBase(ctx); err != nil {
		t.Fatal(err)
	}
	p2.received.Add(4)
	p2.stored.Add(1)
	got := p2.Stats()
	if got.Received != 94 || got.Stored != 41 {
		t.Errorf("stats with base received=%d stored=%d, want 94/41", got.Received, got.Stored)
	}

	// The next save writes the combined absolute snapshot, so a restart
	// never loses today's counters.
	if err := p2.Save(); err != nil {
		t.Fatal(err)
	}
	row, found, err := storage.DailyStatsForDay(ctx, p2.db, today)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if row.Received != 94 || row.Stored != 41 {
		t.Errorf("saved received=%d stored=%d, want 94/41", row.Received, row.Stored)
	}
}

func TestRestoreBaseMissingRowIsNotAnError(t *testing.T) {
	p := newStatsPipeline(t)
	if err := p.RestoreBase(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The day is recorded even when the row is missing so a later
	// midnight rollover can attribute pre-midnight session counters to
	// the correct day; the counters themselves stay zero.
	if p.baseDay != utcDay(time.Now()) || p.base.Received != 0 {
		t.Errorf("base must stay zero for a missing row: day=%q base=%+v", p.baseDay, p.base)
	}
}

func TestRestoreBaseKeepsOrphanReasons(t *testing.T) {
	ctx := context.Background()
	p := newStatsPipeline(t)
	today := utcDay(time.Now())
	// Partial save: reasons committed, stats row missing.
	if err := storage.UpsertDailyReasons(ctx, p.db, today, []storage.ReasonStat{
		{Kind: ReasonKindHTTPReject, Reason: ReasonUnauthorized, Count: 5},
	}); err != nil {
		t.Fatal(err)
	}
	p2 := newStatsPipelineOn(t, p.db)
	if err := p2.RestoreBase(ctx); err != nil {
		t.Fatal(err)
	}
	got := p2.ReasonCounts()
	if got[ReasonKindHTTPReject][ReasonUnauthorized] != 5 {
		t.Errorf("orphan reasons lost: %v", got)
	}
}
