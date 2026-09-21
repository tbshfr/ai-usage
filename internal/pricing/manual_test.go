package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestManualPricingFallbackAndSnapshots(t *testing.T) {
	db := seedtest.EmptyDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	s := New(db, quiet(), nil)
	s.url = srv.URL
	g := usage("mai", "mai-code-1.1-flash")
	g.InputTokens = ptr(int64(1_000_000))
	g.OutputTokens = ptr(int64(1_000_000))
	g.CacheReadTokens = ptr(int64(250_000))
	insert(t, db, g)
	process(t, s)
	stored := read(t, db, g.ID)
	checkCost(t, stored, 1.355, "manual")
	if stored.CostReportedByHarness || stored.PricingRates == "" || stored.PricingModelID != g.Model || stored.PricingFetchedAt == nil || stored.PricingFetchedAt.Format("2006-01-02") != "2026-09-21" {
		t.Fatalf("manual provenance %+v", stored)
	}
	totals, err := storage.Summary(context.Background(), db, storage.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if totals.CostEstimatedCount != 1 || totals.CostReportedCount != 0 || totals.CostKnownCount != 1 {
		t.Fatalf("manual estimates absent from totals: %+v", totals)
	}
	// Change the file's rates, then enrich an existing request. Saved rates win.
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"models":{"mai-code-1.1-flash":{"inputPerMillion":8,"outputPerMillion":9,"updatedAt":"2026-09-22"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadManualFile(path); err != nil {
		t.Fatal(err)
	}
	g.CacheCreationTokens = ptr(int64(100_000))
	insert(t, db, g)
	process(t, s)
	checkCost(t, read(t, db, g.ID), 1.355, "manual") // cache creation falls back to input rate
	g.ID = "new"
	insert(t, db, g)
	process(t, s)
	checkCost(t, read(t, db, g.ID), 17, "manual")
	g.Cost = ptr(0.0)
	insert(t, db, g)
	process(t, s)
	checkCost(t, read(t, db, g.ID), 0, "harness")
}

func TestManualPrecedenceAndValidation(t *testing.T) {
	s := New(nil, quiet(), nil)
	g := usage("mai", "mai-code-1.1-flash")
	manual := s.enrich(g)
	if manual.CostSource != "manual" {
		t.Fatalf("missing bundled MAI price %+v", manual)
	}
	s.catalog = catalog{"microsoft/mai-code-1.1-flash": {Prompt: 1, Completion: 2}}
	s.fetched = time.Now()
	checkCost(t, s.enrich(g), 140, "openrouter")
	s.manual["custom-free"] = manualPrice{Rates: rates{Prompt: 1, Completion: 2}, UpdatedAt: time.Now()}
	g.Model = "custom-free"
	checkCost(t, s.enrich(g), 0, "free")
	g.CostReportedByHarness = true
	g.CostSource = "harness"
	g.Cost = ptr(5.0)
	checkCost(t, s.enrich(g), 5, "harness")
	for _, bad := range []string{
		`{}`, `{"models":null}`, `{"models":{}} {}`,
		`{"models":{"x":{"inputPerMillion":1,"updatedAt":"2026-01-01"}}}`,
		`{"models":{"x":{"inputPerMillion":1,"outputPerMillion":-1,"updatedAt":"2026-01-01"}}}`,
		`{"models":{"x":{"inputPerMillion":1,"outputPerMillion":2,"updatedAt":"invalid"}}}`,
		`{"models":{"x":{"inputPerMillion":1,"outputPerMillion":2,"cachedInput":3,"updatedAt":"2026-01-01"}}}`,
	} {
		if _, err := parseManualPrices([]byte(bad)); err == nil {
			t.Errorf("accepted malformed prices %s", bad)
		}
	}
	prices, err := parseManualPrices([]byte(`{"models":{"x":{"inputPerMillion":1,"outputPerMillion":2,"cacheReadPerMillion":0,"cacheWritePerMillion":3,"reasoningPerMillion":4,"updatedAt":"2026-01-01"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := prices["x"].Rates
	if r.CacheRead != 0 || math.Abs(r.CacheWrite-3e-6) > 1e-15 || math.Abs(r.Reasoning-4e-6) > 1e-15 {
		t.Fatalf("optional rates %+v", r)
	}
	if err := s.LoadManualFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing configured file accepted")
	}
}
