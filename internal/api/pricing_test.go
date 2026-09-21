package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestPricingProvenanceTotalsAndUI(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, source := range []string{"harness", "openrouter", "manual", "free", "unknown"} {
		g := normalize.Generation{ID: source, Timestamp: stamp, Source: "codex", Model: "test", ConversationID: "conversation"}
		if source == "harness" {
			v := 2.0
			g.Cost = &v
		}
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
		if source == "openrouter" || source == "manual" || source == "free" {
			v := 0.0
			if source == "openrouter" || source == "manual" {
				v = 3
				if source == "manual" {
					v = 4
				}
				g.PricingModelID = "vendor/test"
				g.PricingFetchedAt = &stamp
			}
			g.Cost = &v
			g.CostSource = source
			if _, err := storage.ApplyPricing(ctx, db, g); err != nil {
				t.Fatal(err)
			}
		}
	}
	srv := newServer(t, db, nil)
	for _, path := range []string{"/api/summary", "/api/sources", "/api/providers", "/api/models", "/api/timeseries?bucket=day", "/api/timeseries?bucket=week", "/api/timeseries?bucket=month"} {
		status, body := get(t, srv.URL+path)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, body)
		}
		var row summaryResponse
		if path == "/api/summary" {
			if err := json.Unmarshal([]byte(body), &row); err != nil {
				t.Fatal(err)
			}
		} else {
			var rows []summaryResponse
			if err := json.Unmarshal([]byte(body), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("%s rows %s", path, body)
			}
			row = rows[0]
		}
		if row.CostReportedCount != 1 || row.CostEstimatedCount != 2 || row.CostFreeCount != 1 || row.CostKnownCount != 4 || row.CostTotal == nil || *row.CostTotal != 9 {
			t.Fatalf("%s totals %+v", path, row)
		}
	}
	for _, source := range []string{"harness", "openrouter", "manual", "free", "unknown"} {
		_, body := get(t, srv.URL+"/api/generations/"+source)
		var g generation
		if err := json.Unmarshal([]byte(body), &g); err != nil {
			t.Fatal(err)
		}
		if g.CostSource != source || g.CostReportedByHarness != (source == "harness") {
			t.Fatalf("provenance %+v", g)
		}
		if (source == "openrouter" || source == "manual") && (g.PricingFetchedAt == nil || !g.PricingFetchedAt.Equal(stamp) || g.PricingModelID != "vendor/test") {
			t.Fatalf("pricing %+v", g)
		}
	}
	for _, tc := range []struct{ path, expected string }{
		{"/generations/openrouter", "(estimated)"},
		{"/generations/openrouter", "Prices fetched"},
		{"/generations/manual", "(manual estimate)"},
		{"/generations/manual", "Prices updated"},
		{"/generations/free", "(free)"},
		{"/generations/harness", "(reported)"},
		{"/generations/unknown", "<dt>Cost</dt><dd>—</dd>"},
		{"/breakdowns?range=all", `<abbr class="cost-estimate" title="Includes estimated costs from OpenRouter or manual rates">≈</abbr>`},
		{"/sessions?range=all", `<abbr class="cost-estimate" title="Includes estimated costs from OpenRouter or manual rates">≈</abbr>`},
	} {
		status, body := get(t, srv.URL+tc.path)
		if status != http.StatusOK || !strings.Contains(body, tc.expected) {
			t.Errorf("%s: status %d, missing %q", tc.path, status, tc.expected)
		}
		if strings.Contains(body, "(includes estimates)") {
			t.Errorf("%s: old estimate label remains", tc.path)
		}
	}
}
