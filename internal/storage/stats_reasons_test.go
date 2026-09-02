package storage

import (
	"context"
	"testing"
)

func TestUpsertDailyReasonsIsAbsoluteSnapshot(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	day := "2026-08-01"

	if err := UpsertDailyReasons(ctx, db, day, []ReasonStat{
		{Kind: "http_reject", Reason: "unauthorized", Count: 3},
		{Kind: "rejected", Reason: "no_source", Count: 5},
	}); err != nil {
		t.Fatal(err)
	}
	// A second save replaces the day's rows entirely (absolute snapshot,
	// never additive), and drops reasons that no longer appear.
	if err := UpsertDailyReasons(ctx, db, day, []ReasonStat{
		{Kind: "http_reject", Reason: "unauthorized", Count: 7},
	}); err != nil {
		t.Fatal(err)
	}

	rows, found, err := DailyReasonsForDay(ctx, db, day)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if len(rows) != 1 || rows[0].Kind != "http_reject" || rows[0].Reason != "unauthorized" || rows[0].Count != 7 {
		t.Fatalf("rows = %v, want [http_reject/unauthorized 7]", rows)
	}
}

func TestDailyReasonsForDayNotFound(t *testing.T) {
	rows, found, err := DailyReasonsForDay(context.Background(), openStatsDB(t), "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	if found || len(rows) != 0 {
		t.Errorf("found=%v rows=%v, want false/empty for a missing day", found, rows)
	}
}

func TestDailyReasonsForDayOrdered(t *testing.T) {
	ctx := context.Background()
	db := openStatsDB(t)
	if err := UpsertDailyReasons(ctx, db, "2026-08-01", []ReasonStat{
		{Kind: "rejected", Reason: "no_source", Count: 2},
		{Kind: "http_reject", Reason: "bad_gzip", Count: 1},
		{Kind: "rejected", Reason: "not_a_generation", Count: 9},
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := DailyReasonsForDay(ctx, db, "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	want := []ReasonStat{
		{Kind: "http_reject", Reason: "bad_gzip", Count: 1},
		{Kind: "rejected", Reason: "no_source", Count: 2},
		{Kind: "rejected", Reason: "not_a_generation", Count: 9},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}
