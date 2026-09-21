package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPricingMigrationPreservesReportedZeroAndUnknown(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Build a real pre-pricing database, then run the normal upgrade path.
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		v, err := versionOf(f)
		if err != nil {
			t.Fatal(err)
		}
		if v < 5 {
			if err := apply(db, v, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO generations(id,timestamp,source,cost,created_at) VALUES ('zero',1,'opencode',0,1),('paid',1,'opencode',2,1),('unknown',1,'codex',NULL,1)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"zero", "paid", "unknown"} {
		g, ok, err := GenerationByID(context.Background(), db, id)
		if err != nil || !ok {
			t.Fatalf("read %v %v", ok, err)
		}
		if id == "unknown" {
			if g.Cost != nil || g.CostSource != "unknown" || g.CostReportedByHarness {
				t.Fatalf("unknown %+v", g)
			}
		} else {
			if g.Cost == nil || !g.CostReportedByHarness || g.CostSource != "harness" {
				t.Fatalf("reported %+v", g)
			}
			if id == "zero" && *g.Cost != 0 {
				t.Fatal("reported zero changed")
			}
		}
	}
	pending, err := PendingPricing(context.Background(), db, "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("history must belong to one-time job: %v %v", pending, err)
	}
}

func TestManualPricingMigrationPreservesEstimatesAndBackfillMarker(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "v5.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		v, err := versionOf(f)
		if err != nil {
			t.Fatal(err)
		}
		if v <= 5 {
			if err := apply(db, v, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO generations(id,timestamp,source,cost,created_at,cost_source,pricing_rates,pricing_pending,pricing_revision) VALUES ('estimate',1,'codex',2,1,'openrouter','{}',1,7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pricing_jobs VALUES ('historical-costs-v1',123)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	g, _, err := GenerationByID(context.Background(), db, "estimate")
	if err != nil {
		t.Fatal(err)
	}
	if g.Cost == nil || *g.Cost != 2 || g.CostSource != "openrouter" || g.PricingRates != "{}" || g.PricingRevision != 7 {
		t.Fatalf("estimate altered %+v", g)
	}
	var completed int64
	if err := db.QueryRow(`SELECT completed_at FROM pricing_jobs WHERE name='historical-costs-v1'`).Scan(&completed); err != nil || completed != 123 {
		t.Fatalf("backfill marker altered %d %v", completed, err)
	}
	pending, err := PendingPricing(context.Background(), db, "")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending queue altered %v %v", pending, err)
	}
	g.CostSource = "manual"
	if _, err := ApplyPricing(context.Background(), db, *g); err != nil {
		t.Fatal(err)
	}
}
