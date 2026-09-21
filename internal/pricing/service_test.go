package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func ptr[T any](v T) *T   { return &v }
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func usage(id, model string) normalize.Generation {
	return normalize.Generation{ID: id, Source: "codex", Model: model, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), InputTokens: ptr(int64(100)), OutputTokens: ptr(int64(20))}
}
func insert(t *testing.T, db *sql.DB, g normalize.Generation) {
	t.Helper()
	if _, err := storage.InsertGeneration(context.Background(), db, g); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, db *sql.DB, id string) normalize.Generation {
	t.Helper()
	g, ok, err := storage.GenerationByID(context.Background(), db, id)
	if err != nil || !ok {
		t.Fatalf("read %s: %v, found %v", id, err, ok)
	}
	return *g
}
func process(t *testing.T, s *Service) {
	t.Helper()
	if err := s.process(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func checkCost(t *testing.T, g normalize.Generation, want float64, source string) {
	t.Helper()
	if g.Cost == nil || math.Abs(*g.Cost-want) > 1e-10 || g.CostSource != source {
		t.Fatalf("%s: cost %v source %s, want %g %s", g.ID, g.Cost, g.CostSource, want, source)
	}
}

const priceBody = `{"data":[{"id":"openai/test","pricing":{"prompt":"0.01","completion":"0.02","input_cache_read":"0.001","input_cache_write":"0.015","request":"0.1"}}]}`

func TestCatalogMatchingAndInvalidPrices(t *testing.T) {
	c, err := parseCatalog([]byte(`{"data":[
 {"id":"anthropic/claude-3.5-sonnet","pricing":{"prompt":"0.1","completion":"0.2"}},
 {"id":"vendor/unique","pricing":{"prompt":"0","completion":"0"}},
 {"id":"one/ambiguous","pricing":{"prompt":"1","completion":"2"}},
 {"id":"two/ambiguous","pricing":{"prompt":"1","completion":"2"}},
 {"id":"bad/negative","pricing":{"prompt":"-1","completion":"1"}},
 {"id":"bad/nan","pricing":{"prompt":"NaN","completion":"1"}},
 {"id":"bad/inf","pricing":{"prompt":"1","completion":"Inf"}},
 {"id":"bad/cache","pricing":{"prompt":"1","completion":"1","input_cache_read":"-1"}},
 {"id":"bad/missing","pricing":{"prompt":"1"}}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, want string }{
		{"anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"claude-3-5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"unique", "vendor/unique"}, {"ambiguous", ""}, {"other/unique", ""}, {"unknown", ""}, {"bad/negative", ""}, {"bad/nan", ""}, {"bad/inf", ""}, {"bad/cache", ""}, {"bad/missing", ""},
	} {
		id, _, ok := c.match(tc.model)
		if id != tc.want || ok != (tc.want != "") {
			t.Errorf("match %s = %s %v", tc.model, id, ok)
		}
	}
	r := c["anthropic/claude-3.5-sonnet"]
	if r.CacheRead != .1 || r.CacheWrite != .1 || r.Reasoning != .2 || r.Request != 0 {
		t.Fatalf("fallback rates %+v", r)
	}
	for _, body := range []string{`{`, `{"data":[]}`, `{"data":[{"id":"x","pricing":{"prompt":"bad"}}]}`} {
		if _, err := parseCatalog([]byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestTokenArithmeticAndFreePrecedence(t *testing.T) {
	r := rates{Prompt: 1, Completion: 2, CacheRead: .1, CacheWrite: 1.5, Reasoning: 3, Request: 4}
	for _, tc := range []struct {
		source string
		want   float64
	}{{"codex", 136}, {"copilot", 146}, {"opencode", 176}} {
		g := usage("x", "test")
		g.Source = tc.source
		g.CacheReadTokens = ptr(int64(20))
		g.CacheCreationTokens = ptr(int64(10))
		g.ReasoningTokens = ptr(int64(5))
		// Codex: 70 + 2 + 15 + 15*2 + 5*3 + 4 = 136.
		want := tc.want
		if got := estimate(g, r); got == nil || *got != want {
			t.Errorf("%s = %v want %g", tc.source, got, want)
		}
	}
	g := usage("missing", "test")
	g.OutputTokens = nil
	if estimate(g, r) != nil {
		t.Fatal("incomplete usage must stay unknown")
	}
	s := New(nil, quiet(), nil)
	for _, model := range []string{"vendor/model:free", "model-free", "modelfree", " model-FREE "} {
		g := s.enrich(usage("free", model))
		checkCost(t, g, 0, "free")
		g.Cost = ptr(3.0)
		g.CostReportedByHarness = true
		g.CostSource = "harness"
		checkCost(t, s.enrich(g), 3, "harness")
	}
	if freeModel("free-model") {
		t.Fatal("prefix is not a free suffix")
	}
}

func TestCacheRestartRefreshAndSavedRates(t *testing.T) {
	db := seedtest.EmptyDB(t)
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	body := priceBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) }))
	defer srv.Close()
	notifications := 0
	service := func() *Service {
		s := New(db, quiet(), func() { notifications++ })
		s.url = srv.URL
		s.now = func() time.Time { return now }
		return s
	}
	s := service()
	insert(t, db, usage("first", "test"))
	process(t, s)
	first := read(t, db, "first")
	checkCost(t, first, 1.5, "openrouter")
	if first.CostReportedByHarness || first.PricingModelID != "openai/test" || !first.PricingFetchedAt.Equal(now) || first.PricingRates == "" {
		t.Fatalf("provenance %+v", first)
	}
	s = service()
	now = now.Add(24*time.Hour - time.Second)
	insert(t, db, usage("cached", "test"))
	process(t, s)
	if calls != 1 {
		t.Fatalf("cache across restart: %d calls", calls)
	}
	now = now.Add(time.Second)
	body = `{"data":[{"id":"openai/test","pricing":{"prompt":"0.1","completion":"0.2"}}]}`
	insert(t, db, usage("new-price", "test"))
	process(t, s)
	checkCost(t, read(t, db, "new-price"), 14, "openrouter")
	checkCost(t, read(t, db, "first"), 1.5, "openrouter")
	if calls != 2 {
		t.Fatalf("expiration: %d calls", calls)
	}
	// Enrichment of an old request must reuse its original rates.
	extra := usage("first", "test")
	extra.CacheReadTokens = ptr(int64(20))
	insert(t, db, extra)
	process(t, s)
	checkCost(t, read(t, db, "first"), 1.32, "openrouter")
	// A later harness cost, including zero, supersedes the estimate.
	extra.Cost = ptr(0.0)
	insert(t, db, extra)
	process(t, s)
	got := read(t, db, "first")
	checkCost(t, got, 0, "harness")
	if !got.CostReportedByHarness || got.PricingRates != "" || got.PricingFetchedAt != nil {
		t.Fatalf("harness provenance %+v", got)
	}
	extra.Cost = ptr(9.0)
	insert(t, db, extra)
	process(t, s)
	checkCost(t, read(t, db, "first"), 0, "harness")
	if notifications < 3 {
		t.Fatal("missing notifications")
	}
}

func TestFailureCooldownStalePricesAndRecovery(t *testing.T) {
	db := seedtest.EmptyDB(t)
	now := time.Now()
	calls := 0
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if fail {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, priceBody)
	}))
	defer srv.Close()
	s := New(db, quiet(), nil)
	s.url = srv.URL
	s.now = func() time.Time { return now }
	insert(t, db, usage("pending", "test"))
	insert(t, db, usage("free", "whatever-free"))
	process(t, s)
	checkCost(t, read(t, db, "free"), 0, "free")
	if read(t, db, "pending").Cost != nil {
		t.Fatal("no catalog must leave cost unknown")
	}
	process(t, s)
	if calls != 1 {
		t.Fatal("failure should be throttled")
	}
	fail = false
	now = now.Add(5 * time.Minute)
	process(t, s)
	checkCost(t, read(t, db, "pending"), 1.5, "openrouter")
	stamp := *read(t, db, "pending").PricingFetchedAt
	fail = true
	now = now.Add(24 * time.Hour)
	insert(t, db, usage("stale", "test"))
	process(t, s)
	got := read(t, db, "stale")
	checkCost(t, got, 1.5, "openrouter")
	if !got.PricingFetchedAt.Equal(stamp) {
		t.Fatal("stale fetch timestamp changed")
	}
}

func TestWorkerCoalescesAndStops(t *testing.T) {
	db := seedtest.EmptyDB(t)
	var calls atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, priceBody)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	changed := make(chan struct{}, 2)
	s := New(db, quiet(), func() { changed <- struct{}{} })
	s.url = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	if calls.Load() != 0 {
		t.Fatal("startup must not fetch")
	}
	insert(t, db, usage("a", "test"))
	s.Notify()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no fetch")
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(s.Notify)
	}
	wg.Wait()
	close(release)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no enrichment notification")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	if calls.Load() != 1 {
		t.Fatalf("coalescing: %d calls", calls.Load())
	}
}

func TestPricingCannotOverwriteConcurrentHarnessReport(t *testing.T) {
	db := seedtest.EmptyDB(t)
	g := usage("race", "test")
	insert(t, db, g)
	old := read(t, db, g.ID)
	old.Cost = ptr(2.0)
	old.CostSource = "openrouter"
	g.Cost = ptr(3.0)
	insert(t, db, g)
	changed, err := storage.ApplyPricing(context.Background(), db, old)
	if err != nil || changed {
		t.Fatalf("stale update %v %v", changed, err)
	}
	checkCost(t, read(t, db, g.ID), 3, "harness")
}

func TestConditionalRatesAndFixtureAliases(t *testing.T) {
	c, err := parseCatalog([]byte(`{"data":[
 {"id":"anthropic/claude-haiku-4.5","pricing":{"prompt":"1","completion":"2","overrides":[{"min_prompt_tokens":100,"prompt":"3","completion":"4"}]}},
 {"id":"anthropic/claude-sonnet-4.5","pricing":{"prompt":"1","completion":"2"}}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"claude-haiku-4-5-20251001", "claude-haiku-4-5", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929"} {
		if _, _, ok := c.match(model); !ok {
			t.Errorf("missing alias %s", model)
		}
	}
	_, r, _ := c.match("claude-haiku-4-5-20251001")
	g := usage("threshold", "test")
	g.CacheReadTokens = ptr(int64(30))
	g.ReasoningTokens = ptr(int64(5))
	if got := estimate(g, r); got == nil || *got != 140 {
		t.Fatalf("at threshold = %v", got)
	}
	g.InputTokens = ptr(int64(101))
	if got := estimate(g, r); got == nil || *got != 383 {
		t.Fatalf("above threshold = %v", got)
	}
	c, err = parseCatalog([]byte(`{"data":[{"id":"time/model","pricing":{"prompt":"1","completion":"2","overrides":[
 {"utc_start":1630,"utc_end":30,"utc_days":["monday"],"prompt":"3"},
 {"min_prompt_tokens":50,"completion":"4"},
 {"new_unknown_condition":true,"prompt":"999"}
 ]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stamp string
		want  float64
	}{
		{"2026-09-21T16:29:00Z", 180},
		{"2026-09-21T16:30:00Z", 380},
		{"2026-09-21T00:29:00Z", 380},
		{"2026-09-21T00:30:00Z", 180},
		{"2026-09-22T17:00:00Z", 180},
	} {
		g.Timestamp, _ = time.Parse(time.RFC3339, tc.stamp)
		g.InputTokens = ptr(int64(100))
		g.CacheReadTokens = nil
		g.ReasoningTokens = nil
		if got := estimate(g, c["time/model"]); got == nil || *got != tc.want {
			t.Errorf("%s = %v want %g", tc.stamp, got, tc.want)
		}
	}
}

func TestWorkerCancelsInflightFetch(t *testing.T) {
	db := seedtest.EmptyDB(t)
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	s := New(db, quiet(), nil)
	s.url = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	insert(t, db, usage("pending", "test"))
	s.Notify()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no fetch")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel fetch")
	}
	if read(t, db, "pending").Cost != nil {
		t.Fatal("canceled fetch priced usage")
	}
}

func TestQwenMaxAlias(t *testing.T) {
	c, err := parseCatalog([]byte(`{"data":[{"id":"qwen/qwen3.8-max-0902","pricing":{"prompt":"0.000002","completion":"0.000006"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"qwen3.8-max", "qwen/qwen3.8-max", "qwen3.8-max-0902"} {
		id, _, ok := c.match(model)
		if !ok || id != "qwen/qwen3.8-max-0902" {
			t.Errorf("match %s = %s %v", model, id, ok)
		}
	}
	// If the catalog begins listing the exact ID, it takes priority over the alias.
	c["qwen/qwen3.8-max"] = rates{Prompt: 1, Completion: 2}
	if id, _, ok := c.match("qwen/qwen3.8-max"); !ok || id != "qwen/qwen3.8-max" {
		t.Fatalf("exact ID lost priority: %s %v", id, ok)
	}
}
