package storage_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func assertCost(t *testing.T, got *float64, known int64, want float64, wantKnown int64, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: CostTotal is nil, want %v", label, want)
	}
	if !almostEqual(*got, want) {
		t.Errorf("%s: CostTotal = %v, want %v", label, *got, want)
	}
	if known != wantKnown {
		t.Errorf("%s: CostKnownCount = %d, want %d", label, known, wantKnown)
	}
}

func TestSummaryMixedCost(t *testing.T) {
	db := seedtest.DB(t)
	s, err := storage.Summary(context.Background(), db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	want := storage.SummaryResult{
		Requests:            20,
		InputTokens:         1977, // copilot uncached: 1885 (c5 700→300, c12 40→30) + opencode 92
		OutputTokens:        1204,
		CacheReadTokens:     410,
		CacheCreationTokens: 56,
		ReasoningTokens:     35,
		CostReportedCount:   8,
		CostKnownCount:      8,
		CostUnknownCount:    12,
	}
	cmp := s
	cmp.CostTotal = nil
	if cmp != want {
		t.Errorf("Summary = %+v, want %+v", cmp, want)
	}
	if s.CostTotal == nil {
		t.Fatal("CostTotal is nil, want 2.85 (mixed cost set)")
	}
	if !almostEqual(*s.CostTotal, 2.85) {
		t.Errorf("CostTotal = %v, want 2.85", *s.CostTotal)
	}
}

func TestSummaryCopilotOnlyCostStaysNull(t *testing.T) {
	db := seedtest.DB(t)
	f := seedtest.FullRange()
	f.Source = "copilot"
	s, err := storage.Summary(context.Background(), db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 12 {
		t.Errorf("Requests = %d, want 12", s.Requests)
	}
	if s.CostTotal != nil {
		t.Errorf("CostTotal = %v, want nil: no copilot row reports cost, 0 would be a lie", *s.CostTotal)
	}
	if s.CostKnownCount != 0 || s.CostUnknownCount != 12 {
		t.Errorf("cost known/unknown = %d/%d, want 0/12", s.CostKnownCount, s.CostUnknownCount)
	}
}

func TestSummaryExactFilters(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	f := seedtest.FullRange()
	f.Provider = "anthropic"
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 8 || s.InputTokens != 92 || s.OutputTokens != 184 || s.CacheCreationTokens != 56 {
		t.Errorf("anthropic summary = %+v", s)
	}
	assertCost(t, s.CostTotal, s.CostKnownCount, 2.85, 8, "anthropic")

	f = seedtest.FullRange()
	f.Model = "claude-sonnet-4-5"
	s, err = storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 || s.InputTokens != 50 || s.OutputTokens != 20 || s.ReasoningTokens != 5 {
		t.Errorf("model=claude-sonnet-4-5 summary = %+v", s)
	}
	if s.CostTotal != nil {
		t.Errorf("CostTotal = %v, want nil", *s.CostTotal)
	}
}

func TestFilterNormalization(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	if _, err := storage.Summary(ctx, db, storage.Filter{From: seedtest.FullRange().To, To: seedtest.FullRange().From}); err == nil {
		t.Error("To before From must error")
	}

	f := storage.Filter{To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatalf("zero From must mean no lower bound: %v", err)
	}
	if s.Requests != 6 {
		t.Errorf("Requests = %d, want 6 (jan31 + 2024 leap rows, no lower bound)", s.Requests)
	}
	assertCost(t, s.CostTotal, s.CostKnownCount, 0.85, 3, "before feb01")

	s, err = storage.Summary(ctx, db, storage.Filter{})
	if err != nil {
		t.Fatalf("zero To must mean now: %v", err)
	}
	if s.Requests != 20 {
		t.Errorf("Requests = %d, want 20 (all seeded rows are in the past)", s.Requests)
	}
}

func TestCacheHitRate(t *testing.T) {
	// input is the canonical uncached prompt (telemetry.md Q1): the full
	// prompt is input + cache tokens under every source's convention.
	// 800 cached of 1000+800+100 prompt tokens:
	if r := storage.CacheHitRate(1000, 800, 100); r == nil || !almostEqual(*r, 800.0/1900.0) {
		t.Errorf("CacheHitRate(1000, 800, 100) = %v, want %v", r, 800.0/1900.0)
	}
	r := storage.CacheHitRate(3425, 3520, 0)
	if r == nil || !almostEqual(*r, 3520.0/6945.0) {
		t.Errorf("CacheHitRate(3425, 3520, 0) = %v, want %v", r, 3520.0/6945.0)
	}
	// prompts but no cache activity is an honest 0.0%, not nil
	if r := storage.CacheHitRate(260, 0, 0); r == nil || *r != 0 {
		t.Errorf("CacheHitRate(260, 0, 0) = %v, want 0", r)
	}
	// no tokens reported at all: nil, never a fabricated 0%
	if r := storage.CacheHitRate(0, 0, 0); r != nil {
		t.Errorf("CacheHitRate(0, 0, 0) = %v, want nil", r)
	}
}

func TestSummaryCacheHitRate(t *testing.T) {
	db := seedtest.DB(t)
	s, err := storage.Summary(context.Background(), db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	// 1977 uncached input, 410 read, 56 creation → full prompt 2443
	if r := s.CacheHitRate(); r == nil || !almostEqual(*r, 410.0/2443.0) {
		t.Errorf("summary hit rate = %v, want %v", r, 410.0/2443.0)
	}
}

func TestConversationFilter(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	for _, tc := range []struct {
		conv string
		want int64
	}{
		{"conv-copilot", 12},
		{"conv-opencode", 8},
		{storage.ConversationNone, 0}, // every seeded row has a conversation ID
		{"", 20},
	} {
		f := seedtest.FullRange()
		f.Conversation = tc.conv
		s, err := storage.Summary(ctx, db, f)
		if err != nil {
			t.Fatal(err)
		}
		if s.Requests != tc.want {
			t.Errorf("conversation=%q: Requests = %d, want %d", tc.conv, s.Requests, tc.want)
		}
	}

	// a Copilot title generation has no conversation ID
	title := normalize.Generation{
		ID:           "t1",
		Timestamp:    time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		Source:       "copilot",
		Provider:     "github",
		Model:        "gpt-4o-mini-2024-07-18",
		InputTokens:  seedtest.IP(260),
		OutputTokens: seedtest.IP(4),
		AgentName:    "title",
	}
	if _, err := storage.InsertGeneration(ctx, db, title); err != nil {
		t.Fatal(err)
	}
	f := seedtest.FullRange()
	f.Conversation = storage.ConversationNone
	s, err := storage.Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 {
		t.Errorf("conversation=none after title insert: Requests = %d, want 1", s.Requests)
	}
	rows, err := storage.RecentGenerations(ctx, db, f, storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "t1" || rows[0].AgentName != "title" {
		t.Errorf("conversation=none rows = %+v, want the title generation", rows)
	}
}

func TestTimeseriesDayBuckets(t *testing.T) {
	db := seedtest.DB(t)
	pts, err := storage.Timeseries(context.Background(), db, seedtest.FullRange(), storage.BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	want := []storage.TimeseriesPoint{
		{BucketStart: mid(2024, 2, 29), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostReportedCount: 1, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 31), Requests: 4, InputTokens: 311, OutputTokens: 142, CacheCreationTokens: 5, ReasoningTokens: 30, CostReportedCount: 2, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 1), Requests: 4, InputTokens: 183, OutputTokens: 121, CacheCreationTokens: 13, CostReportedCount: 2, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 28), Requests: 3, InputTokens: 313, OutputTokens: 226, CacheReadTokens: 400, CacheCreationTokens: 8, CostReportedCount: 1, CostKnownCount: 1},                  // c5 cache folded out of input: 700→300
		{BucketStart: mid(2026, 3, 1), Requests: 3, InputTokens: 94, OutputTokens: 128, CacheReadTokens: 10, CacheCreationTokens: 9, ReasoningTokens: 5, CostReportedCount: 1, CostKnownCount: 1}, // c12 40→30
		{BucketStart: mid(2026, 3, 2), Requests: 4, InputTokens: 60, OutputTokens: 55, CacheCreationTokens: 10, CostReportedCount: 1, CostKnownCount: 1},
	}
	if len(pts) != len(want) {
		t.Fatalf("got %d day buckets, want %d: %+v", len(pts), len(want), pts)
	}
	for i, w := range want {
		got := pts[i]
		got.CostTotal = nil
		if got != w {
			t.Errorf("bucket %d = %+v, want %+v", i, got, w)
		}
	}
	costs := []float64{0.70, 0.15, 0.50, 0.40, 0.50, 0.60}
	for i, c := range costs {
		if pts[i].CostTotal == nil || !almostEqual(*pts[i].CostTotal, c) {
			t.Errorf("bucket %d CostTotal = %v, want %v", i, pts[i].CostTotal, c)
		}
	}
}

func TestTimeseriesWeekBuckets(t *testing.T) {
	db := seedtest.DB(t)
	pts, err := storage.Timeseries(context.Background(), db, seedtest.FullRange(), storage.BucketWeek)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	// Monday-anchored weeks: leap week, jan31+feb01 (same Mon-anchored week),
	// feb28+mar01 (Sat+Sun, same week, spanning the month boundary), mar02.
	want := []storage.TimeseriesPoint{
		{BucketStart: mid(2024, 2, 26), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostReportedCount: 1, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 26), Requests: 8, InputTokens: 494, OutputTokens: 263, CacheCreationTokens: 18, ReasoningTokens: 30, CostReportedCount: 4, CostKnownCount: 4},
		{BucketStart: mid(2026, 2, 23), Requests: 6, InputTokens: 407, OutputTokens: 354, CacheReadTokens: 410, CacheCreationTokens: 17, ReasoningTokens: 5, CostReportedCount: 2, CostKnownCount: 2},
		{BucketStart: mid(2026, 3, 2), Requests: 4, InputTokens: 60, OutputTokens: 55, CacheCreationTokens: 10, CostReportedCount: 1, CostKnownCount: 1},
	}
	if len(pts) != len(want) {
		t.Fatalf("got %d week buckets, want %d: %+v", len(pts), len(want), pts)
	}
	for i, w := range want {
		got := pts[i]
		got.CostTotal = nil
		if got != w {
			t.Errorf("week bucket %d = %+v, want %+v", i, got, w)
		}
	}
	costs := []float64{0.70, 0.65, 0.90, 0.60}
	for i, c := range costs {
		if pts[i].CostTotal == nil || !almostEqual(*pts[i].CostTotal, c) {
			t.Errorf("week bucket %d CostTotal = %v, want %v", i, pts[i].CostTotal, c)
		}
	}
}

func TestTimeseriesMonthBuckets(t *testing.T) {
	db := seedtest.DB(t)
	pts, err := storage.Timeseries(context.Background(), db, seedtest.FullRange(), storage.BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	want := []storage.TimeseriesPoint{
		{BucketStart: mid(2024, 2, 1), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostReportedCount: 1, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 1), Requests: 4, InputTokens: 311, OutputTokens: 142, CacheCreationTokens: 5, ReasoningTokens: 30, CostReportedCount: 2, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 1), Requests: 7, InputTokens: 496, OutputTokens: 347, CacheReadTokens: 400, CacheCreationTokens: 21, CostReportedCount: 3, CostKnownCount: 3},
		{BucketStart: mid(2026, 3, 1), Requests: 7, InputTokens: 154, OutputTokens: 183, CacheReadTokens: 10, CacheCreationTokens: 19, ReasoningTokens: 5, CostReportedCount: 2, CostKnownCount: 2},
	}
	if len(pts) != len(want) {
		t.Fatalf("got %d month buckets, want %d: %+v", len(pts), len(want), pts)
	}
	for i, w := range want {
		got := pts[i]
		got.CostTotal = nil
		if got != w {
			t.Errorf("month bucket %d = %+v, want %+v", i, got, w)
		}
	}
	costs := []float64{0.70, 0.15, 0.90, 1.10}
	for i, c := range costs {
		if pts[i].CostTotal == nil || !almostEqual(*pts[i].CostTotal, c) {
			t.Errorf("month bucket %d CostTotal = %v, want %v", i, pts[i].CostTotal, c)
		}
	}
}

func TestTimeseriesInvalidBucket(t *testing.T) {
	db := seedtest.DB(t)
	if _, err := storage.Timeseries(context.Background(), db, seedtest.FullRange(), "year"); err == nil {
		t.Error("invalid bucket must error")
	}
}

func breakdownByKey(t *testing.T, rows []storage.Breakdown) map[string]storage.Breakdown {
	t.Helper()
	m := make(map[string]storage.Breakdown, len(rows))
	for _, r := range rows {
		if _, dup := m[r.Key]; dup {
			t.Fatalf("duplicate key %q in breakdown", r.Key)
		}
		m[r.Key] = r
	}
	return m
}

func TestBySourceAndProvider(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	src, err := storage.BySource(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	m := breakdownByKey(t, src)
	copilot, ok := m["copilot"]
	if !ok {
		t.Fatal("missing copilot breakdown")
	}
	wantCopilot := storage.Breakdown{
		Key: "copilot", Requests: 12, InputTokens: 1885, OutputTokens: 1020,
		CacheReadTokens: 410, ReasoningTokens: 35, CostUnknownCount: 12,
	}
	if copilot != wantCopilot {
		t.Errorf("copilot = %+v, want %+v", copilot, wantCopilot)
	}
	if copilot.CostTotal != nil {
		t.Errorf("copilot CostTotal = %v, want nil", *copilot.CostTotal)
	}

	prov, err := storage.ByProvider(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	m = breakdownByKey(t, prov)
	anthropic, ok := m["anthropic"]
	if !ok {
		t.Fatal("missing anthropic breakdown")
	}
	if anthropic.Requests != 8 || anthropic.CacheCreationTokens != 56 {
		t.Errorf("anthropic = %+v", anthropic)
	}
	assertCost(t, anthropic.CostTotal, anthropic.CostKnownCount, 2.85, 8, "anthropic")
}

func TestByModelOrderingAndSparseSums(t *testing.T) {
	db := seedtest.DB(t)
	rows, err := storage.ByModel(context.Background(), db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	// ordered by total tokens desc: 1960, 1345, 332, 80
	wantKeys := []string{"gpt-5.6-luna", "gpt-4.1", "claude-haiku-4-5-20251001", "claude-sonnet-4-5"}
	if len(rows) != len(wantKeys) {
		t.Fatalf("got %d models, want %d: %+v", len(rows), len(wantKeys), rows)
	}
	for i, k := range wantKeys {
		if rows[i].Key != k {
			t.Errorf("position %d = %q, want %q", i, rows[i].Key, k)
		}
	}
	if rows[0].Requests != 4 || rows[0].CacheReadTokens != 10 {
		t.Errorf("gpt-5.6-luna = %+v (sparse cacheRead SUM must be 10, nils ignored)", rows[0])
	}
	if rows[1].Requests != 7 {
		t.Errorf("gpt-4.1 Requests = %d, want 7 (includes token-less c11)", rows[1].Requests)
	}
	haiku := rows[2]
	assertCost(t, haiku.CostTotal, haiku.CostKnownCount, 2.85, 8, "haiku")
	if haiku.CostUnknownCount != 0 {
		t.Errorf("haiku CostUnknownCount = %d, want 0", haiku.CostUnknownCount)
	}
	if rows[3].CostTotal != nil {
		t.Errorf("claude-sonnet-4-5 CostTotal = %v, want nil", *rows[3].CostTotal)
	}
}

func TestByModelMergesOpenRouterCreatorPrefix(t *testing.T) {
	db := seedtest.EmptyDB(t)
	ctx := context.Background()
	for _, g := range []normalize.Generation{
		{ID: "openrouter-glm", Timestamp: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Source: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash", InputTokens: seedtest.IP(10), Cost: seedtest.FP(0.25)},
		{ID: "direct-glm", Timestamp: time.Date(2026, 3, 1, 0, 1, 0, 0, time.UTC), Source: "opencode", Provider: "z-ai", Model: "glm-5.3-flash", InputTokens: seedtest.IP(20), OutputTokens: seedtest.IP(5)},
		{ID: "other-prefixed", Timestamp: time.Date(2026, 3, 1, 0, 2, 0, 0, time.UTC), Source: "opencode", Provider: "other", Model: "z-ai/glm-5.3-flash", InputTokens: seedtest.IP(3)},
	} {
		if _, err := storage.InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := storage.ByModel(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	byKey := breakdownByKey(t, rows)
	if len(byKey) != 2 {
		t.Fatalf("models = %+v, want 2 groups", rows)
	}
	glm := byKey["glm-5.3-flash"]
	if glm.Requests != 2 || glm.InputTokens != 30 || glm.OutputTokens != 5 || glm.CostKnownCount != 1 || glm.CostUnknownCount != 1 {
		t.Errorf("merged glm = %+v", glm)
	}
	assertCost(t, glm.CostTotal, glm.CostKnownCount, 0.25, 1, "merged glm")
	if other := byKey["z-ai/glm-5.3-flash"]; other.Requests != 1 || other.InputTokens != 3 {
		t.Errorf("non-OpenRouter prefixed model = %+v", other)
	}

	f := seedtest.FullRange()
	f.Model = "z-ai/glm-5.3-flash"
	f.Provider = "openrouter"
	filtered, err := storage.ByModel(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Key != "glm-5.3-flash" || filtered[0].Requests != 1 {
		t.Errorf("raw model filter = %+v", filtered)
	}
}

func TestDistinctValues(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	sources, err := storage.DistinctSources(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0] != "copilot" || sources[1] != "opencode" {
		t.Errorf("sources = %v", sources)
	}

	providers, err := storage.DistinctProviders(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0] != "anthropic" || providers[1] != "github" {
		t.Errorf("providers = %v", providers)
	}

	models, err := storage.DistinctModels(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-haiku-4-5-20251001", "claude-sonnet-4-5", "gpt-4.1", "gpt-5.6-luna"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("models[%d] = %q, want %q", i, models[i], want[i])
		}
	}

	narrow := seedtest.FullRange()
	narrow.From = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	narrow.Source = "opencode"
	models, err = storage.DistinctModels(ctx, db, narrow)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "claude-haiku-4-5-20251001" {
		t.Errorf("filtered models = %v, want only haiku", models)
	}
}

func TestRecentGenerationsPagination(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	page1, err := storage.RecentGenerations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 5 {
		t.Fatalf("got %d rows, want 5", len(page1))
	}
	// newest first: all mar02 rows (12:06..12:15), then the newest mar01 row.
	wantIDs := []string{"o6", "c9", "c8", "c7", "c12"}
	for i, id := range wantIDs {
		if page1[i].ID != id {
			t.Errorf("page1[%d] = %q, want %q", i, page1[i].ID, id)
		}
	}
	if page1[0].Cost == nil || !almostEqual(*page1[0].Cost, 0.60) {
		t.Errorf("o6 cost = %v, want 0.60", page1[0].Cost)
	}

	page2, err := storage.RecentGenerations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID != "c12" || page2[1].ID != "o5" {
		t.Errorf("page2 = %+v, want [c12 o5]", page2)
	}

	all, err := storage.RecentGenerations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 20 {
		t.Errorf("got %d rows, want 20 (dedup o1 must not appear twice)", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Timestamp.Before(all[i].Timestamp) {
			t.Errorf("rows not ordered by timestamp DESC at %d", i)
		}
	}

	if _, err := storage.RecentGenerations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 0, 0); err == nil {
		t.Error("limit 0 must error")
	}
	if _, err := storage.RecentGenerations(ctx, db, seedtest.FullRange(), storage.OrderDesc, 5, -1); err == nil {
		t.Error("negative offset must error")
	}
}

func TestGenerationByID(t *testing.T) {
	db := seedtest.DB(t)
	ctx := context.Background()

	g, ok, err := storage.GenerationByID(ctx, db, "o1")
	if err != nil || !ok {
		t.Fatalf("GenerationByID(o1): ok=%v err=%v", ok, err)
	}
	if g.Source != "opencode" || g.Provider != "anthropic" {
		t.Errorf("source/provider = %q/%q", g.Source, g.Provider)
	}
	if g.InputTokens == nil || *g.InputTokens != 10 || g.OutputTokens == nil || *g.OutputTokens != 20 {
		t.Errorf("tokens = %v/%v", g.InputTokens, g.OutputTokens)
	}
	if g.CacheCreationTokens == nil || *g.CacheCreationTokens != 5 {
		t.Errorf("cache_creation = %v, want 5", g.CacheCreationTokens)
	}
	if g.Cost == nil || !almostEqual(*g.Cost, 0.10) {
		t.Errorf("cost = %v, want 0.10", g.Cost)
	}
	if g.Timestamp != time.Date(2026, 1, 31, 0, 10, 0, 0, time.UTC) {
		t.Errorf("timestamp = %v, want 2026-01-31T00:10:00Z UTC", g.Timestamp)
	}

	c1, ok, err := storage.GenerationByID(ctx, db, "c1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if c1.Cost != nil || c1.CacheCreationTokens != nil {
		t.Errorf("copilot nil fields must round-trip as nil, got cost=%v cache=%v", c1.Cost, c1.CacheCreationTokens)
	}
	if c1.ReasoningTokens != nil {
		t.Errorf("c1 reasoning = %v, want nil", c1.ReasoningTokens)
	}

	if _, ok, err := storage.GenerationByID(ctx, db, "missing"); ok || err != nil {
		t.Errorf("missing id: ok=%v err=%v, want false/nil", ok, err)
	}
}

// Migrations, inserts, and queries must agree on column names and types.
func TestMigrationContinuityInsertQuery(t *testing.T) {
	db := seedtest.DB(t)
	rows := seedtest.Rows(t)
	ctx := context.Background()

	// pick the sparse row: only input tokens set
	g, ok, err := storage.GenerationByID(ctx, db, "c4")
	if err != nil || !ok {
		t.Fatalf("GenerationByID(c4): ok=%v err=%v", ok, err)
	}
	want := rows[3]
	if g.ID != want.ID || g.Timestamp != want.Timestamp || g.Source != want.Source ||
		g.Model != want.Model || g.InputTokens == nil || *g.InputTokens != 10 {
		t.Errorf("c4 round-trip mismatch: got %+v, want %+v", g, want)
	}
	if g.OutputTokens != nil || g.Cost != nil || g.ReasoningTokens != nil {
		t.Errorf("c4 must round-trip NULLs as nil: %+v", g)
	}

	s, err := storage.Summary(ctx, db, seedtest.FullRange())
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 20 {
		t.Errorf("Requests = %d, want 20", s.Requests)
	}
}
