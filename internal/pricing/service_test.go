package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	}{{"codex", 136}, {"copilot", 136}, {"opencode", 176}} {
		g := usage("x", "test")
		g.Source = tc.source
		g.CacheReadTokens = ptr(int64(20))
		g.CacheCreationTokens = ptr(int64(10))
		g.ReasoningTokens = ptr(int64(5))
		// Codex and Copilot: 70 + 2 + 15 + 15*2 + 5*3 + 4 = 136.
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

func TestCopilotReasoningUpgradeRepricesSavedEstimate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	g := usage("old-copilot", "test")
	g.Source = normalize.SourceCopilot
	g.ReasoningTokens = ptr(int64(5))
	insert(t, db, g)
	priced := read(t, db, g.ID)
	priced.Cost = ptr(155.0) // Old formula billed all 20 output tokens plus 5 reasoning tokens.
	priced.CostSource = "openrouter"
	priced.PricingModelID = "openai/test"
	priced.PricingRates = `{"prompt":1,"completion":2,"cache_read":1,"cache_write":1,"reasoning":3}`
	if changed, err := storage.ApplyPricing(ctx, db, priced); err != nil || !changed {
		t.Fatalf("save old estimate: changed=%v err=%v", changed, err)
	}
	harness := g
	harness.ID = "reported-copilot"
	harness.Cost = ptr(88.0)
	insert(t, db, harness)
	other := g
	other.ID = "old-opencode"
	other.Source = normalize.SourceOpenCode
	insert(t, db, other)
	otherPrice := read(t, db, other.ID)
	otherPrice.Cost = ptr(155.0)
	otherPrice.CostSource = "openrouter"
	otherPrice.PricingRates = priced.PricingRates
	if changed, err := storage.ApplyPricing(ctx, db, otherPrice); err != nil || !changed {
		t.Fatalf("save other estimate: changed=%v err=%v", changed, err)
	}
	// Reapply the new migration to a database shaped like an older installation.
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 6`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := storage.PendingPricing(ctx, db, "")
	if err != nil || len(pending) != 1 || pending[0].ID != g.ID {
		t.Fatalf("upgrade pending = %v, err = %v", pending, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := New(db, quiet(), nil)
	s.url = server.URL // Saved rates must suffice during a catalog outage.
	process(t, s)
	updated := read(t, db, g.ID)
	checkCost(t, updated, 145, "openrouter")
	if updated.PricingModelID != "openai/test" || updated.PricingRates != priced.PricingRates {
		t.Fatalf("repricing lost its original rate provenance: %+v", updated)
	}
	checkCost(t, read(t, db, harness.ID), 88, "harness")
	checkCost(t, read(t, db, other.ID), 155, "openrouter")
	pending, err = storage.PendingPricing(ctx, db, "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("repriced row still pending = %v, err = %v", pending, err)
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

func TestWorkerResumesPendingAndRetriesWithoutNewUsage(t *testing.T) {
	db := seedtest.EmptyDB(t)
	insert(t, db, usage("pending-after-restart", "test"))
	var calls atomic.Int64
	firstAttempt := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			firstAttempt <- struct{}{}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, priceBody)
	}))
	defer srv.Close()
	changed := make(chan struct{}, 1)
	s := New(db, quiet(), func() { changed <- struct{}{} })
	s.url = srv.URL
	s.retryDelay = 25 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-firstAttempt:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not resume pending pricing")
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("failed catalog fetch was not retried without new usage")
	}
	checkCost(t, read(t, db, "pending-after-restart"), 1.5, "openrouter")
	if got := calls.Load(); got != 2 {
		t.Fatalf("catalog fetches = %d, want failed attempt and one retry", got)
	}
}

func TestEarlierTimestampRepricesConditionalEstimate(t *testing.T) {
	db := seedtest.EmptyDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"openai/test","pricing":{"prompt":"1","completion":"2","overrides":[{"utc_days":["thursday"],"utc_start":0,"utc_end":100,"prompt":"3"}]}}]}`)
	}))
	defer srv.Close()
	s := New(db, quiet(), nil)
	s.url = srv.URL
	g := usage("earlier-timestamp", "test") // Thursday, 2026-01-01 00:00 UTC.
	insert(t, db, g)
	process(t, s)
	checkCost(t, read(t, db, g.ID), 340, "openrouter")

	g.Timestamp = g.Timestamp.Add(-time.Hour) // Wednesday, outside the override.
	insert(t, db, g)
	pending, err := storage.PendingPricing(context.Background(), db, "")
	if err != nil || len(pending) != 1 {
		t.Fatalf("earlier timestamp must requeue estimate: %d pending, %v", len(pending), err)
	}
	process(t, s)
	checkCost(t, read(t, db, g.ID), 140, "openrouter")
	insert(t, db, g)
	pending, err = storage.PendingPricing(context.Background(), db, "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("identical retry requeued pricing: %d pending, %v", len(pending), err)
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

// TestOverridePriceKeysAndDayCasing pins the override grammar of the
// OpenRouter API: condition fields are evaluated, price fields reuse the
// base pricing keys as JSON strings (the estimate ignores units it does not
// bill), and any other value is an unrecognized condition that skips the
// entry. Real catalogs carry audio, input_audio_cache, and
// input_cache_write_1h price keys inside overrides.
func TestOverridePriceKeysAndDayCasing(t *testing.T) {
	c, err := parseCatalog([]byte(`{"data":[{"id":"unit/model","pricing":{"prompt":"1","completion":"2","request":"0","overrides":[
	{"min_prompt_tokens":100,"prompt":"3","audio":"0.000004","input_audio_cache":"0.0000004","input_cache_write_1h":"0.000012"},
	{"utc_days":["Thursday","SUNDAY"],"completion":"9"},
	{"image":true,"prompt":"999"},
	{"future_unit":"0.001","request":"0.5"}
	]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	thursday := usage("x", "test") // 2026-01-01 is a Thursday
	for _, tc := range []struct {
		override string
		want     bool
	}{
		{`{"utc_days":["Thursday","SUNDAY"]}`, true},
		{`{"utc_days":["friday"]}`, false},
		{`{"image":true,"prompt":"999"}`, false},
		{`{"future_unit":"0.001","request":"0.5"}`, true},
	} {
		var o map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.override), &o); err != nil {
			t.Fatal(err)
		}
		if got := overrideMatches(o, thursday); got != tc.want {
			t.Errorf("overrideMatches(%s) = %v, want %v", tc.override, got, tc.want)
		}
	}
	withoutInput := thursday
	withoutInput.InputTokens = nil
	var threshold map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"min_prompt_tokens":100,"prompt":"3"}`), &threshold); err != nil {
		t.Fatal(err)
	}
	if overrideMatches(threshold, withoutInput) {
		t.Error("threshold override must not match without input tokens")
	}
	// Unmodeled unit prices (audio, future_unit) must not skip their entry:
	// 100*1 + 20*9 + 0.5 request.
	r := c["unit/model"]
	if got := estimate(thursday, r); got == nil || *got != 280.5 {
		t.Fatalf("at threshold = %v, want 280.5", got)
	}
	// Above the threshold the token prices come from the first entry:
	// 101*3 + 20*9 + 0.5.
	above := thursday
	above.InputTokens = ptr(int64(101))
	if got := estimate(above, r); got == nil || *got != 483.5 {
		t.Fatalf("above threshold = %v, want 483.5", got)
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
