package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

func TestPricingSnapshotsDeduplicateAndRejectStaleUpdates(t *testing.T) {
	ctx := context.Background()
	db := openedDB(t)
	for _, id := range []string{"first", "second", "stale"} {
		if _, err := InsertGeneration(ctx, db, normalize.Generation{
			ID: id, Source: "codex", Model: "test", Timestamp: time.UnixMilli(1000),
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) normalize.Generation {
		g, ok, err := GenerationByID(ctx, db, id)
		if err != nil || !ok {
			t.Fatalf("read %s: %v, found %v", id, err, ok)
		}
		return *g
	}
	for _, id := range []string{"first", "second"} {
		g := get(id)
		g.Cost = ptr(1.0)
		g.CostSource = "openrouter"
		g.PricingRates = `{"prompt":0.01}`
		if changed, err := ApplyPricing(ctx, db, g); err != nil || !changed {
			t.Fatalf("price %s: changed=%v err=%v", id, changed, err)
		}
	}
	var firstID, secondID, count int
	if err := db.QueryRow(`SELECT pricing_snapshot_id FROM generations WHERE id = 'first'`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT pricing_snapshot_id FROM generations WHERE id = 'second'`).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if firstID != secondID || get("second").PricingRates != `{"prompt":0.01}` {
		t.Fatalf("identical estimates must share a readable snapshot: %d, %d", firstID, secondID)
	}

	stale := get("stale")
	stale.Cost = ptr(2.0)
	stale.CostSource = "openrouter"
	stale.PricingRates = `{"prompt":0.02}`
	stale.PricingRevision++
	if changed, err := ApplyPricing(ctx, db, stale); err != nil || changed {
		t.Fatalf("stale update: changed=%v err=%v", changed, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pricing_snapshots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || get("stale").Cost != nil {
		t.Fatalf("stale update left snapshot or cost: count=%d", count)
	}
}

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
	if _, err := db.Exec(`UPDATE generations SET cost_source = 'manual' WHERE id = 'unknown'`); err != nil {
		t.Fatalf("pricing migration must allow manual estimates: %v", err)
	}
}
