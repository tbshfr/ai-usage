package storage

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// seedRows returns a fixed mix of ~20 generations (computed by hand):
//   - copilot rows (cost NULL, various models, one legacy-reasoning row)
//   - opencode rows (cost set, cache_creation set)
//   - a multi-round trace (c8/c9 share a trace id)
//   - one sparse row (only input tokens: c4) and one token-less row (c11)
//   - rows across a month boundary (jan31/feb01, feb28/mar01) and a leap day
//
// The seed helper re-inserts o1 to cover the dedup path.
func seedRows(t *testing.T) []normalize.Generation {
	t.Helper()
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d.UTC()
	}
	type spec struct {
		id, source, provider, model, day string
		in, out, cacheRead, cacheCreate  *int64
		reasoning                        *int64
		cost                             *float64
	}
	specs := []spec{
		{id: "c1", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-01-31", in: ip(100), out: ip(50)},
		{id: "c2", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-01-31", in: ip(200), out: ip(100), reasoning: ip(30)},
		{id: "c3", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-01", in: ip(150), out: ip(75)},
		{id: "c4", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-02-01", in: ip(10)},
		{id: "c5", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-28", in: ip(300), out: ip(200), cacheRead: ip(400)},
		{id: "c6", source: "copilot", provider: "github", model: "claude-sonnet-4-5", day: "2026-03-01", in: ip(50), out: ip(25), reasoning: ip(5)},
		{id: "c7", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: ip(20), out: ip(10)},
		{id: "c8", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: ip(20), out: ip(10)},
		{id: "c9", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-03-02", in: ip(5), out: ip(5)},
		{id: "c10", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2024-02-29", in: ip(1000), out: ip(500)},
		{id: "o1", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-01-31", in: ip(10), out: ip(20), cacheCreate: ip(5), cost: fp(0.10)},
		{id: "o2", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-01", in: ip(11), out: ip(22), cacheCreate: ip(6), cost: fp(0.20)},
		{id: "o3", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-01", in: ip(12), out: ip(24), cacheCreate: ip(7), cost: fp(0.30)},
		{id: "o4", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-02-28", in: ip(13), out: ip(26), cacheCreate: ip(8), cost: fp(0.40)},
		{id: "o5", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-03-01", in: ip(14), out: ip(28), cacheCreate: ip(9), cost: fp(0.50)},
		{id: "o6", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-03-02", in: ip(15), out: ip(30), cacheCreate: ip(10), cost: fp(0.60)},
		{id: "o7", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2024-02-29", in: ip(16), out: ip(32), cacheCreate: ip(11), cost: fp(0.70)},
		{id: "c11", source: "copilot", provider: "github", model: "gpt-4.1", day: "2026-02-28"},
		{id: "o8", source: "opencode", provider: "anthropic", model: "claude-haiku-4-5-20251001", day: "2026-01-31", in: ip(1), out: ip(2), cost: fp(0.05)},
		{id: "c12", source: "copilot", provider: "github", model: "gpt-5.6-luna", day: "2026-03-01", in: ip(40), out: ip(80), cacheRead: ip(10)},
	}
	out := make([]normalize.Generation, 0, len(specs))
	for i, s := range specs {
		traceID := "trace-" + s.id
		if s.id == "c8" || s.id == "c9" {
			traceID = "trace-multi-round"
		}
		out = append(out, normalize.Generation{
			ID:                  s.id,
			Timestamp:           day(s.day).Add(time.Duration(i) * time.Minute),
			Source:              s.source,
			ServiceName:         s.source,
			Provider:            s.provider,
			Model:               s.model,
			InputTokens:         s.in,
			OutputTokens:        s.out,
			CacheReadTokens:     s.cacheRead,
			CacheCreationTokens: s.cacheCreate,
			ReasoningTokens:     s.reasoning,
			Cost:                s.cost,
			TraceID:             traceID,
			SpanID:              "span-" + s.id,
			ConversationID:      "conv-" + s.source,
		})
	}
	return out
}

func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	rows := seedRows(t)
	for _, g := range rows {
		if _, err := InsertGeneration(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	// dedup case: retried batch must not add a row
	inserted, err := InsertGeneration(ctx, db, rows[10])
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("re-inserting o1 must be deduplicated")
	}
	return db
}

func ip(v int64) *int64     { return &v }
func fp(v float64) *float64 { return &v }

// fullRange covers every seeded row, including the 2024 leap day.
func fullRange() Filter {
	return Filter{
		From: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
}

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
	db := seedDB(t)
	s, err := Summary(context.Background(), db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	want := SummaryResult{
		Requests:            20,
		InputTokens:         1987,
		OutputTokens:        1239,
		CacheReadTokens:     410,
		CacheCreationTokens: 56,
		ReasoningTokens:     35,
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
	db := seedDB(t)
	f := fullRange()
	f.Source = "copilot"
	s, err := Summary(context.Background(), db, f)
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
	db := seedDB(t)
	ctx := context.Background()

	f := fullRange()
	f.Provider = "anthropic"
	s, err := Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 8 || s.InputTokens != 92 || s.OutputTokens != 184 || s.CacheCreationTokens != 56 {
		t.Errorf("anthropic summary = %+v", s)
	}
	assertCost(t, s.CostTotal, s.CostKnownCount, 2.85, 8, "anthropic")

	f = fullRange()
	f.Model = "claude-sonnet-4-5"
	s, err = Summary(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 1 || s.InputTokens != 50 || s.OutputTokens != 25 || s.ReasoningTokens != 5 {
		t.Errorf("model=claude-sonnet-4-5 summary = %+v", s)
	}
	if s.CostTotal != nil {
		t.Errorf("CostTotal = %v, want nil", *s.CostTotal)
	}
}

func TestFilterNormalization(t *testing.T) {
	db := seedDB(t)
	ctx := context.Background()

	if _, err := Summary(ctx, db, Filter{From: fullRange().To, To: fullRange().From}); err == nil {
		t.Error("To before From must error")
	}

	f := Filter{To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	s, err := Summary(ctx, db, f)
	if err != nil {
		t.Fatalf("zero From must mean no lower bound: %v", err)
	}
	if s.Requests != 6 {
		t.Errorf("Requests = %d, want 6 (jan31 + 2024 leap rows, no lower bound)", s.Requests)
	}
	assertCost(t, s.CostTotal, s.CostKnownCount, 0.85, 3, "before feb01")

	s, err = Summary(ctx, db, Filter{})
	if err != nil {
		t.Fatalf("zero To must mean now: %v", err)
	}
	if s.Requests != 20 {
		t.Errorf("Requests = %d, want 20 (all seeded rows are in the past)", s.Requests)
	}
}

func TestTimeseriesDayBuckets(t *testing.T) {
	db := seedDB(t)
	pts, err := Timeseries(context.Background(), db, fullRange(), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	want := []TimeseriesPoint{
		{BucketStart: mid(2024, 2, 29), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 31), Requests: 4, InputTokens: 311, OutputTokens: 172, CacheCreationTokens: 5, ReasoningTokens: 30, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 1), Requests: 4, InputTokens: 183, OutputTokens: 121, CacheCreationTokens: 13, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 28), Requests: 3, InputTokens: 313, OutputTokens: 226, CacheReadTokens: 400, CacheCreationTokens: 8, CostKnownCount: 1},
		{BucketStart: mid(2026, 3, 1), Requests: 3, InputTokens: 104, OutputTokens: 133, CacheReadTokens: 10, CacheCreationTokens: 9, ReasoningTokens: 5, CostKnownCount: 1},
		{BucketStart: mid(2026, 3, 2), Requests: 4, InputTokens: 60, OutputTokens: 55, CacheCreationTokens: 10, CostKnownCount: 1},
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
	db := seedDB(t)
	pts, err := Timeseries(context.Background(), db, fullRange(), BucketWeek)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	// Monday-anchored weeks: leap week, jan31+feb01 (same Mon-anchored week),
	// feb28+mar01 (Sat+Sun, same week, spanning the month boundary), mar02.
	want := []TimeseriesPoint{
		{BucketStart: mid(2024, 2, 26), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 26), Requests: 8, InputTokens: 494, OutputTokens: 293, CacheCreationTokens: 18, ReasoningTokens: 30, CostKnownCount: 4},
		{BucketStart: mid(2026, 2, 23), Requests: 6, InputTokens: 417, OutputTokens: 359, CacheReadTokens: 410, CacheCreationTokens: 17, ReasoningTokens: 5, CostKnownCount: 2},
		{BucketStart: mid(2026, 3, 2), Requests: 4, InputTokens: 60, OutputTokens: 55, CacheCreationTokens: 10, CostKnownCount: 1},
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
	db := seedDB(t)
	pts, err := Timeseries(context.Background(), db, fullRange(), BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(y int, m time.Month, d int) int64 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	want := []TimeseriesPoint{
		{BucketStart: mid(2024, 2, 1), Requests: 2, InputTokens: 1016, OutputTokens: 532, CacheCreationTokens: 11, CostKnownCount: 1},
		{BucketStart: mid(2026, 1, 1), Requests: 4, InputTokens: 311, OutputTokens: 172, CacheCreationTokens: 5, ReasoningTokens: 30, CostKnownCount: 2},
		{BucketStart: mid(2026, 2, 1), Requests: 7, InputTokens: 496, OutputTokens: 347, CacheReadTokens: 400, CacheCreationTokens: 21, CostKnownCount: 3},
		{BucketStart: mid(2026, 3, 1), Requests: 7, InputTokens: 164, OutputTokens: 188, CacheReadTokens: 10, CacheCreationTokens: 19, ReasoningTokens: 5, CostKnownCount: 2},
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
	db := seedDB(t)
	if _, err := Timeseries(context.Background(), db, fullRange(), "hour"); err == nil {
		t.Error("invalid bucket must error")
	}
}

func breakdownByKey(t *testing.T, rows []Breakdown) map[string]Breakdown {
	t.Helper()
	m := make(map[string]Breakdown, len(rows))
	for _, r := range rows {
		if _, dup := m[r.Key]; dup {
			t.Fatalf("duplicate key %q in breakdown", r.Key)
		}
		m[r.Key] = r
	}
	return m
}

func TestBySourceAndProvider(t *testing.T) {
	db := seedDB(t)
	ctx := context.Background()

	src, err := BySource(ctx, db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	m := breakdownByKey(t, src)
	copilot, ok := m["copilot"]
	if !ok {
		t.Fatal("missing copilot breakdown")
	}
	wantCopilot := Breakdown{
		Key: "copilot", Requests: 12, InputTokens: 1895, OutputTokens: 1055,
		CacheReadTokens: 410, ReasoningTokens: 35, CostUnknownCount: 12,
	}
	if copilot != wantCopilot {
		t.Errorf("copilot = %+v, want %+v", copilot, wantCopilot)
	}
	if copilot.CostTotal != nil {
		t.Errorf("copilot CostTotal = %v, want nil", *copilot.CostTotal)
	}

	prov, err := ByProvider(ctx, db, fullRange())
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
	db := seedDB(t)
	rows, err := ByModel(context.Background(), db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	// ordered by total tokens desc: 1970, 1345, 332, 80
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

func TestDistinctValues(t *testing.T) {
	db := seedDB(t)
	ctx := context.Background()

	sources, err := DistinctSources(ctx, db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0] != "copilot" || sources[1] != "opencode" {
		t.Errorf("sources = %v", sources)
	}

	providers, err := DistinctProviders(ctx, db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0] != "anthropic" || providers[1] != "github" {
		t.Errorf("providers = %v", providers)
	}

	models, err := DistinctModels(ctx, db, fullRange())
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

	narrow := fullRange()
	narrow.From = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	narrow.Source = "opencode"
	models, err = DistinctModels(ctx, db, narrow)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "claude-haiku-4-5-20251001" {
		t.Errorf("filtered models = %v, want only haiku", models)
	}
}

func TestRecentGenerationsPagination(t *testing.T) {
	db := seedDB(t)
	ctx := context.Background()

	page1, err := RecentGenerations(ctx, db, fullRange(), 5, 0)
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

	page2, err := RecentGenerations(ctx, db, fullRange(), 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID != "c12" || page2[1].ID != "o5" {
		t.Errorf("page2 = %+v, want [c12 o5]", page2)
	}

	all, err := RecentGenerations(ctx, db, fullRange(), 100, 0)
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

	if _, err := RecentGenerations(ctx, db, fullRange(), 0, 0); err == nil {
		t.Error("limit 0 must error")
	}
	if _, err := RecentGenerations(ctx, db, fullRange(), 5, -1); err == nil {
		t.Error("negative offset must error")
	}
}

func TestGenerationByID(t *testing.T) {
	db := seedDB(t)
	ctx := context.Background()

	g, ok, err := GenerationByID(ctx, db, "o1")
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

	c1, ok, err := GenerationByID(ctx, db, "c1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if c1.Cost != nil || c1.CacheCreationTokens != nil {
		t.Errorf("copilot nil fields must round-trip as nil, got cost=%v cache=%v", c1.Cost, c1.CacheCreationTokens)
	}
	if c1.ReasoningTokens != nil {
		t.Errorf("c1 reasoning = %v, want nil", c1.ReasoningTokens)
	}

	if _, ok, err := GenerationByID(ctx, db, "missing"); ok || err != nil {
		t.Errorf("missing id: ok=%v err=%v, want false/nil", ok, err)
	}
}

// Migration continuity: Phase 2 migrations + InsertGeneration + query layer
// must agree on column names and types.
func TestMigrationContinuityInsertQuery(t *testing.T) {
	db := seedDB(t)
	rows := seedRows(t)
	ctx := context.Background()

	// pick the sparse row: only input tokens set
	g, ok, err := GenerationByID(ctx, db, "c4")
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

	s, err := Summary(ctx, db, fullRange())
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 20 {
		t.Errorf("Requests = %d, want 20", s.Requests)
	}
}
