package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

const dayMs = 86400000

// maxDistinctValues and maxBreakdownGroups bound filter dropdowns and
// breakdown tables so one OTLP writer sending many unique model/provider
// values cannot permanently inflate every reader's page size. Values beyond
// the cap are omitted from dropdowns/groups until cleanup.
const (
	maxDistinctValues  = 1000
	maxBreakdownGroups = 1000
)

// Bucket selects the timeseries granularity. Hour and day are grouped
// directly in SQL; week (Monday-anchored) and month (calendar month) merge
// UTC day rows in Go. All bucketing is done in UTC (the API layer documents
// this so the UI can label it).
type Bucket string

const (
	BucketHour  Bucket = "hour"
	BucketDay   Bucket = "day"
	BucketWeek  Bucket = "week"
	BucketMonth Bucket = "month"
)

// valid reports whether b is a known bucket.
func (b Bucket) valid() bool {
	switch b {
	case BucketHour, BucketDay, BucketWeek, BucketMonth:
		return true
	}
	return false
}

// expr is the SQL grouping expression: UTC hour or UTC day starts.
func (b Bucket) expr() string {
	if b == BucketHour {
		return "timestamp / 3600000 * 3600000"
	}
	return "timestamp / 86400000 * 86400000"
}

// directSQL is true when SQL rows are already the wanted buckets (hour,
// day); week and month merge day rows in Go.
func (b Bucket) directSQL() bool { return b == BucketHour || b == BucketDay }

// SpanSeconds is the nominal bucket width in seconds, used by the UI to
// keep the time axis meaningful when the range holds a single bucket.
func (b Bucket) SpanSeconds() int64 {
	switch b {
	case BucketHour:
		return 3600
	case BucketDay:
		return 86400
	case BucketWeek:
		return 7 * 86400
	default:
		return 30 * 86400
	}
}

// SummaryResult holds totals for a filter range. CostTotal is nil when no
// row in the range has a known cost ("no cost data" must never surface as 0).
type SummaryResult struct {
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostReportedCount   int64
	CostEstimatedCount  int64
	CostFreeCount       int64
	CostKnownCount      int64
	CostTotal           *float64
	CostUnknownCount    int64
}

// TimeseriesPoint is one bucket of a timeseries.
type TimeseriesPoint struct {
	BucketStart         int64
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostReportedCount   int64
	CostEstimatedCount  int64
	CostFreeCount       int64
	CostKnownCount      int64
	CostTotal           *float64
}

// Breakdown is one key (source, provider, or model) of a group-by query.
type Breakdown struct {
	Key                 string
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostReportedCount   int64
	CostEstimatedCount  int64
	CostFreeCount       int64
	CostKnownCount      int64
	CostUnknownCount    int64
	CostTotal           *float64
}

// CacheHitRate returns the fraction of prompt tokens served from cache.
//
// Input must be the canonical uncached prompt (see uncachedInputSQL):
// the full prompt is input + cacheRead + cacheCreation under both token
// conventions, so one formula serves every aggregate, mixed sources
// included. Nil means no prompt tokens were reported at all (denominator
// zero); prompts with zero cache reads report an honest 0.0%.
func CacheHitRate(input, cacheRead, cacheCreation int64) *float64 {
	denom := input + cacheRead + cacheCreation
	if denom <= 0 {
		return nil
	}
	rate := float64(cacheRead) / float64(denom)
	// Clamp defense-in-depth: with validated non-negative inputs rate is
	// already in [0,1]; clamp so legacy/edge rows can never render -10% etc.
	if rate < 0 {
		rate = 0
	} else if rate > 1 {
		rate = 1
	}
	return &rate
}

// uncachedInputSQL is the canonical prompt input as a per-row SQL
// expression, summed by every aggregate query. Copilot and Codex report the
// prompt count including cached tokens (OpenAI-style), so cached parts are
// subtracted there; OpenCode and Maki report input separately from cache. Stored
// rows keep the raw as-reported values. A future source must declare whether
// prompt count includes cache; defaulting to passthrough would double-count
// an OpenAI-style source.
const uncachedInputSQL = `CASE WHEN source IN ('` + normalize.SourceCopilot + `', '` + normalize.SourceCodex + `')
	THEN MAX(COALESCE(input_tokens, 0) - COALESCE(cache_read_tokens, 0)
		- COALESCE(cache_creation_tokens, 0), 0)
	ELSE COALESCE(input_tokens, 0) END`

// outputTokensSQL is the mutually exclusive output bucket. Copilot and Codex
// include reasoning in reported output; OpenCode reports separate output and
// reasoning buckets. Raw rows remain unchanged for provenance.
const outputTokensSQL = `CASE WHEN source IN ('` + normalize.SourceCopilot + `', '` + normalize.SourceCodex + `')
	THEN MAX(COALESCE(output_tokens, 0) - COALESCE(reasoning_tokens, 0), 0)
	ELSE COALESCE(output_tokens, 0) END`

// modelGroupKeySQL keeps OpenRouter's creator prefix out of displayed model
// groups, while retaining raw model IDs for filters and stored records.
const modelGroupKeySQL = `CASE WHEN provider = 'openrouter' AND instr(model, '/') > 1
	THEN substr(model, instr(model, '/') + 1) ELSE model END`

// totalTokensSumSQL is the canonical total across the five disjoint token
// buckets. Use it for model breakdown ordering and podium rankings.
const totalTokensSumSQL = `COALESCE(SUM(` + uncachedInputSQL + `), 0)
	+ COALESCE(SUM(` + outputTokensSQL + `), 0)
	+ COALESCE(SUM(cache_read_tokens), 0)
	+ COALESCE(SUM(cache_creation_tokens), 0)
	+ COALESCE(SUM(reasoning_tokens), 0)`

// CacheHitRate applies CacheHitRate to the aggregate sums; nil when no
// cache/prompt activity was reported.
func (s SummaryResult) CacheHitRate() *float64 {
	return CacheHitRate(s.InputTokens, s.CacheReadTokens, s.CacheCreationTokens)
}

// CacheHitRate applies CacheHitRate to the group's sums.
func (b Breakdown) CacheHitRate() *float64 {
	return CacheHitRate(b.InputTokens, b.CacheReadTokens, b.CacheCreationTokens)
}

// TotalTokens is the sum of all five token columns.
func (s SummaryResult) TotalTokens() int64 {
	return s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreationTokens + s.ReasoningTokens
}

// TotalTokens is the sum of all five token columns.
func (b Breakdown) TotalTokens() int64 {
	return b.InputTokens + b.OutputTokens + b.CacheReadTokens + b.CacheCreationTokens + b.ReasoningTokens
}

// Summary returns totals for the filter range.
func Summary(ctx context.Context, db *sql.DB, f Filter) (SummaryResult, error) {
	f, err := f.normalize(time.Now())
	if err != nil {
		return SummaryResult{}, err
	}
	where, args := f.whereSQL()
	q := `SELECT
	COUNT(*),
	COALESCE(SUM(` + uncachedInputSQL + `), 0),
	COALESCE(SUM(` + outputTokensSQL + `), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0),
	COALESCE(SUM(cost_source = 'harness' AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source IN ('openrouter', 'manual') AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source = 'free' AND cost IS NOT NULL), 0),
	COUNT(cost),
	SUM(cost),
	COUNT(*) - COUNT(cost)
FROM generations WHERE ` + where
	var s SummaryResult
	var costTotal sql.NullFloat64
	if err := db.QueryRowContext(ctx, q, args...).Scan(
		&s.Requests,
		&s.InputTokens,
		&s.OutputTokens,
		&s.CacheReadTokens,
		&s.CacheCreationTokens,
		&s.ReasoningTokens,
		&s.CostReportedCount,
		&s.CostEstimatedCount,
		&s.CostFreeCount,
		&s.CostKnownCount,
		&costTotal,
		&s.CostUnknownCount,
	); err != nil {
		return SummaryResult{}, fmt.Errorf("summary query: %w", err)
	}
	if costTotal.Valid {
		s.CostTotal = &costTotal.Float64
	}
	return s, nil
}

// Earliest returns the timestamp of the first stored generation matching
// the filter's source/provider/model/conversation constraints. The filter's
// From lower bound is ignored so the All time card can label itself
// "tokens since <date>"; To is kept (normalized to now when zero) to match
// the card's upper bound. It returns nil when no row matches (empty DB or
// an overly narrow filter), in which case callers keep the plain "tokens"
// label.
func Earliest(ctx context.Context, db *sql.DB, f Filter) (*time.Time, error) {
	f.From = time.Time{}
	f, err := f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	var min sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT MIN(timestamp) FROM generations WHERE `+where, args...).Scan(&min); err != nil {
		return nil, fmt.Errorf("earliest query: %w", err)
	}
	if !min.Valid {
		return nil, nil
	}
	t := time.UnixMilli(min.Int64).UTC()
	return &t, nil
}

// Timeseries returns per-bucket aggregates. Hour and day buckets are grouped
// in SQL; day rows are merged in Go for week (Monday-anchored) and month
// (calendar month) buckets.
func Timeseries(ctx context.Context, db *sql.DB, f Filter, bucket Bucket) ([]TimeseriesPoint, error) {
	if !bucket.valid() {
		return nil, fmt.Errorf("invalid bucket %q (want hour, day, week, or month)", bucket)
	}
	f, err := f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	expr := bucket.expr()
	q := `SELECT
	` + expr + `,
	COUNT(*),
	COALESCE(SUM(` + uncachedInputSQL + `), 0),
	COALESCE(SUM(` + outputTokensSQL + `), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0),
	COALESCE(SUM(cost_source = 'harness' AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source IN ('openrouter', 'manual') AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source = 'free' AND cost IS NOT NULL), 0),
	COUNT(cost),
	SUM(cost)
FROM generations WHERE ` + where + ` GROUP BY ` + expr + ` ORDER BY 1`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("timeseries query: %w", err)
	}
	defer rows.Close()

	var days []dayRow
	for rows.Next() {
		var d dayRow
		var costTotal sql.NullFloat64
		if err := rows.Scan(
			&d.point.BucketStart,
			&d.point.Requests,
			&d.point.InputTokens,
			&d.point.OutputTokens,
			&d.point.CacheReadTokens,
			&d.point.CacheCreationTokens,
			&d.point.ReasoningTokens,
			&d.point.CostReportedCount,
			&d.point.CostEstimatedCount,
			&d.point.CostFreeCount,
			&d.point.CostKnownCount,
			&costTotal,
		); err != nil {
			return nil, fmt.Errorf("timeseries scan: %w", err)
		}
		if costTotal.Valid {
			d.cost = costTotal.Float64
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeseries rows: %w", err)
	}
	if bucket.directSQL() {
		out := make([]TimeseriesPoint, len(days))
		for i, d := range days {
			pt := d.point
			if d.point.CostKnownCount > 0 {
				pt.CostTotal = &d.cost
			}
			out[i] = pt
		}
		return out, nil
	}
	return mergeDays(days, bucket), nil
}

type dayRow struct {
	point TimeseriesPoint
	cost  float64
}

func mergeDays(days []dayRow, bucket Bucket) []TimeseriesPoint {
	var out []TimeseriesPoint
	var cur *TimeseriesPoint
	var curCost float64
	for _, d := range days {
		start := bucketStart(d.point.BucketStart, bucket)
		if cur == nil || cur.BucketStart != start {
			if cur != nil && cur.CostKnownCount > 0 {
				c := curCost
				cur.CostTotal = &c
			}
			out = append(out, TimeseriesPoint{BucketStart: start})
			cur = &out[len(out)-1]
			curCost = 0
		}
		cur.Requests += d.point.Requests
		cur.InputTokens += d.point.InputTokens
		cur.OutputTokens += d.point.OutputTokens
		cur.CacheReadTokens += d.point.CacheReadTokens
		cur.CacheCreationTokens += d.point.CacheCreationTokens
		cur.ReasoningTokens += d.point.ReasoningTokens
		cur.CostKnownCount += d.point.CostKnownCount
		cur.CostReportedCount += d.point.CostReportedCount
		cur.CostEstimatedCount += d.point.CostEstimatedCount
		cur.CostFreeCount += d.point.CostFreeCount
		curCost += d.cost
	}
	if cur != nil && cur.CostKnownCount > 0 {
		c := curCost
		cur.CostTotal = &c
	}
	return out
}

func bucketStart(dayUnixMs int64, bucket Bucket) int64 {
	t := time.UnixMilli(dayUnixMs).UTC()
	switch bucket {
	case BucketWeek:
		offset := (int(t.Weekday()) + 6) % 7
		return dayUnixMs - int64(offset)*dayMs
	case BucketMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	default:
		return dayUnixMs
	}
}

// BySource returns request/token/cost totals grouped by source, ordered by
// total tokens (sum of all five token columns) descending.
func BySource(ctx context.Context, db *sql.DB, f Filter) ([]Breakdown, error) {
	return breakdown(ctx, db, f, "source")
}

// ByProvider returns totals grouped by raw stored provider value.
func ByProvider(ctx context.Context, db *sql.DB, f Filter) ([]Breakdown, error) {
	return breakdown(ctx, db, f, "provider")
}

// ByModel groups OpenRouter's creator/model IDs with the same bare model ID
// reported by other providers, ordered by total tokens descending. Stored
// models and model filters retain their raw values.
func ByModel(ctx context.Context, db *sql.DB, f Filter) ([]Breakdown, error) {
	return breakdown(ctx, db, f, "model")
}

func breakdown(ctx context.Context, db *sql.DB, f Filter, column string) ([]Breakdown, error) {
	f, err := f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	groupKey := column
	if column == "model" {
		groupKey = modelGroupKeySQL
	}
	q := `SELECT
	COALESCE(` + groupKey + `, ''),
	COUNT(*),
	COALESCE(SUM(` + uncachedInputSQL + `), 0),
	COALESCE(SUM(` + outputTokensSQL + `), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0),
	COALESCE(SUM(cost_source = 'harness' AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source IN ('openrouter', 'manual') AND cost IS NOT NULL), 0),
	COALESCE(SUM(cost_source = 'free' AND cost IS NOT NULL), 0),
	COUNT(cost),
	COUNT(*) - COUNT(cost),
	SUM(cost)
FROM generations WHERE ` + where + ` GROUP BY ` + groupKey
	if column == "model" {
		q += ` ORDER BY ` + totalTokensSumSQL + ` DESC`
	} else {
		q += ` ORDER BY COALESCE(` + column + `, '')`
	}
	q += fmt.Sprintf(` LIMIT %d`, maxBreakdownGroups)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("breakdown query: %w", err)
	}
	defer rows.Close()

	var out []Breakdown
	for rows.Next() {
		var b Breakdown
		var costTotal sql.NullFloat64
		if err := rows.Scan(
			&b.Key,
			&b.Requests,
			&b.InputTokens,
			&b.OutputTokens,
			&b.CacheReadTokens,
			&b.CacheCreationTokens,
			&b.ReasoningTokens,
			&b.CostReportedCount,
			&b.CostEstimatedCount,
			&b.CostFreeCount,
			&b.CostKnownCount,
			&b.CostUnknownCount,
			&costTotal,
		); err != nil {
			return nil, fmt.Errorf("breakdown scan: %w", err)
		}
		if costTotal.Valid {
			b.CostTotal = &costTotal.Float64
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("breakdown rows: %w", err)
	}
	return out, nil
}

// DistinctSources returns the distinct source values in the filter range,
// sorted, for filter dropdowns.
func DistinctSources(ctx context.Context, db *sql.DB, f Filter) ([]string, error) {
	return distinct(ctx, db, f, "source")
}

// DistinctProviders returns the distinct raw provider values, sorted.
func DistinctProviders(ctx context.Context, db *sql.DB, f Filter) ([]string, error) {
	return distinct(ctx, db, f, "provider")
}

// DistinctModels returns the distinct raw model values, sorted.
func DistinctModels(ctx context.Context, db *sql.DB, f Filter) ([]string, error) {
	return distinct(ctx, db, f, "model")
}

func distinct(ctx context.Context, db *sql.DB, f Filter, column string) ([]string, error) {
	f, err := f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT `+column+` FROM generations WHERE `+column+` IS NOT NULL AND `+where+` ORDER BY `+column+fmt.Sprintf(` LIMIT %d`, maxDistinctValues),
		args...)
	if err != nil {
		return nil, fmt.Errorf("distinct query: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("distinct scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("distinct rows: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// RecentGenerations returns full records ordered by timestamp (OrderDesc =
// newest first, the default) for the requests table.
const generationColumns = `
	id, timestamp, source, service_name, provider, model,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	cost, conversation_id, trace_id, span_id, duration_ms,
	agent_name, git_repo, git_branch, cost_reported_by_harness, cost_source,
	COALESCE(pricing_model_id, ''), pricing_fetched_at,
	COALESCE((SELECT rates_json FROM pricing_snapshots WHERE id = pricing_snapshot_id), ''), pricing_revision`

func RecentGenerations(ctx context.Context, db *sql.DB, f Filter, order Order, limit, offset int) ([]normalize.Generation, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive, got %d", limit)
	}
	if offset < 0 {
		return nil, fmt.Errorf("offset must be non-negative, got %d", offset)
	}
	dir, err := order.validate()
	if err != nil {
		return nil, err
	}
	f, err = f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	q := `SELECT ` + generationColumns + ` FROM generations WHERE ` + where + ` ORDER BY timestamp ` + string(dir) + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("recent generations query: %w", err)
	}
	defer rows.Close()

	var out []normalize.Generation
	for rows.Next() {
		g, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent generations rows: %w", err)
	}
	return out, nil
}

// GenerationByID returns the record with the given dedup ID.
func GenerationByID(ctx context.Context, db *sql.DB, id string) (*normalize.Generation, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT `+generationColumns+` FROM generations WHERE id = ?`, id)
	g, err := scanGeneration(row)
	switch {
	case err == nil:
		return g, true, nil
	case err == sql.ErrNoRows:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("generation by id: %w", err)
	}
}

func scanGeneration(row interface{ Scan(dest ...any) error }) (*normalize.Generation, error) {
	var g normalize.Generation
	var serviceName, provider, model, conversationID, traceID, spanID, agentName, gitRepo, gitBranch sql.NullString
	var input, output, cacheRead, cacheCreation, reasoning sql.NullInt64
	var cost sql.NullFloat64
	var duration sql.NullInt64
	var pricingFetchedAt sql.NullInt64
	var timestamp int64
	if err := row.Scan(
		&g.ID,
		&timestamp,
		&g.Source,
		&serviceName,
		&provider,
		&model,
		&input,
		&output,
		&cacheRead,
		&cacheCreation,
		&reasoning,
		&cost,
		&conversationID,
		&traceID,
		&spanID,
		&duration,
		&agentName,
		&gitRepo,
		&gitBranch,
		&g.CostReportedByHarness, &g.CostSource, &g.PricingModelID, &pricingFetchedAt, &g.PricingRates, &g.PricingRevision,
	); err != nil {
		return nil, err
	}
	g.Timestamp = time.UnixMilli(timestamp).UTC()
	if pricingFetchedAt.Valid {
		t := time.UnixMilli(pricingFetchedAt.Int64).UTC()
		g.PricingFetchedAt = &t
	}
	g.ServiceName = serviceName.String
	g.Provider = provider.String
	g.Model = model.String
	g.InputTokens = nullInt64(input)
	g.OutputTokens = nullInt64(output)
	g.CacheReadTokens = nullInt64(cacheRead)
	g.CacheCreationTokens = nullInt64(cacheCreation)
	g.ReasoningTokens = nullInt64(reasoning)
	if cost.Valid {
		g.Cost = &cost.Float64
	}
	g.ConversationID = conversationID.String
	g.TraceID = traceID.String
	g.SpanID = spanID.String
	if duration.Valid {
		g.Duration = time.Duration(duration.Int64) * time.Millisecond
	}
	g.AgentName = agentName.String
	g.GitRepo = gitRepo.String
	g.GitBranch = gitBranch.String
	return &g, nil
}

func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	i := v.Int64
	return &i
}
