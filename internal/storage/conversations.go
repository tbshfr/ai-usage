package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// Order is a sort direction for list queries.
type Order string

const (
	OrderDesc Order = "desc" // newest first (default)
	OrderAsc  Order = "asc"  // oldest first
)

func (o Order) validate() (Order, error) {
	return ParseOrder(string(o))
}

// ParseOrder validates a raw order string; empty means OrderDesc.
func ParseOrder(s string) (Order, error) {
	switch Order(s) {
	case "", OrderDesc:
		return OrderDesc, nil
	case OrderAsc:
		return OrderAsc, nil
	default:
		return "", fmt.Errorf("invalid order %q (want asc or desc)", s)
	}
}

// ConversationSummary aggregates all requests sharing one conversation ID.
// Session-less VS Code activity groups per UTC day instead:
//   - autocomplete (agent XtabProvider): Key "autocomplete:<day-ms>"
//   - title/progress helpers (agents title, progressMessages):
//     Key "titleprogress:<day-ms>"
//   - anything else without a conversation ID: Key "" ("other" group) with
//     Day holding the group's UTC date.
//
// Source/Model/Agent/Repo come from the group's most recent request.
// CostTotal is nil when no row in the group reported cost.
type ConversationSummary struct {
	Key                 string
	Day                 string
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	CostKnownCount      int64
	CostUnknownCount    int64
	CostTotal           *float64
	FirstTimestamp      time.Time
	LastTimestamp       time.Time
	Source              string
	Model               string
	AgentName           string
	GitRepo             string
}

// CacheHitRate applies CacheHitRate to the conversation's sums.
func (c ConversationSummary) CacheHitRate() *float64 {
	return CacheHitRate(c.InputTokens, c.CacheReadTokens, c.CacheCreationTokens)
}

// Other is true for the generic per-day groups of rows without a
// conversation ID (excluding the Autocomplete and Title/progress groups,
// which have their own per-day cards).
func (c ConversationSummary) Other() bool { return c.Key == "" }

// IsAutocomplete is true for per-day VS Code autocomplete groups.
func (c ConversationSummary) IsAutocomplete() bool {
	_, ok := cutPrefix(c.Key, convKeyAutocompletePrefix)
	return ok
}

// IsTitleProgress is true for per-day title/progress helper groups.
func (c ConversationSummary) IsTitleProgress() bool {
	_, ok := cutPrefix(c.Key, convKeyTitleProgressPrefix)
	return ok
}

// Internal group-key prefixes. The autocomplete/titleprogress prefixes match
// their Filter.Conversation sentinels (prefix = sentinel + ":"); "other:"
// maps to the historic "none" sentinel with Key "".
const (
	convKeyOtherPrefix         = "other:"
	convKeyAutocompletePrefix  = "autocomplete:"
	convKeyTitleProgressPrefix = "titleprogress:"
)

// ConversationFilterForKey maps a Conversations group Key to the
// Filter.Conversation value selecting its rows: synthetic per-day keys map
// to their sentinels, "" maps to none, anything else is a real
// conversation ID.
func ConversationFilterForKey(key string) string {
	switch {
	case key == "":
		return ConversationNone
	case hasKeyPrefix(key, convKeyAutocompletePrefix):
		return ConversationAutocomplete
	case hasKeyPrefix(key, convKeyTitleProgressPrefix):
		return ConversationTitleProgress
	default:
		return key
	}
}

func hasKeyPrefix(s, prefix string) bool {
	_, ok := cutPrefix(s, prefix)
	return ok
}

// TotalTokens is the sum of all five token columns.
func (c ConversationSummary) TotalTokens() int64 {
	return c.InputTokens + c.OutputTokens + c.CacheReadTokens + c.CacheCreationTokens + c.ReasoningTokens
}

// Conversations returns per-conversation aggregates ordered by the group's
// last activity (OrderDesc = newest first), with offset pagination. Total is
// the number of groups matching the filter (for the pager).
func Conversations(ctx context.Context, db *sql.DB, f Filter, order Order, limit, offset int) ([]ConversationSummary, int64, error) {
	if limit <= 0 {
		return nil, 0, fmt.Errorf("limit must be positive, got %d", limit)
	}
	if offset < 0 {
		return nil, 0, fmt.Errorf("offset must be non-negative, got %d", offset)
	}
	dir, err := order.validate()
	if err != nil {
		return nil, 0, err
	}
	f, err = f.normalize(time.Now())
	if err != nil {
		return nil, 0, err
	}
	where, args := f.whereSQL()

	// Session-less VS Code agents have no meaningful session, so they group
	// per UTC day under their own agent headers (even when a conversation
	// ID was stored, e.g. rows ingested before normalization started
	// clearing it). The agent match is scoped to source='copilot' because
	// other sources (e.g. opencode) use free-form agent names that may
	// collide with these VS Code values. Everything else without a
	// conversation ID lumps into the generic per-day "other" groups.
	convExpr := `CASE` +
		` WHEN source = '` + normalize.SourceCopilot + `' AND agent_name = '` + normalize.AgentXtabProvider + `' THEN '` + convKeyAutocompletePrefix + `' || (timestamp / 86400000 * 86400000)` +
		` WHEN source = '` + normalize.SourceCopilot + `' AND agent_name IN ('` + normalize.AgentTitle + `', '` + normalize.AgentProgressMessages + `') THEN '` + convKeyTitleProgressPrefix + `' || (timestamp / 86400000 * 86400000)` +
		` WHEN conversation_id IS NULL THEN '` + convKeyOtherPrefix + `' || (timestamp / 86400000 * 86400000)` +
		` ELSE conversation_id END`
	q := `WITH w AS (
	SELECT ` + convExpr + ` AS k,
		ROW_NUMBER() OVER (PARTITION BY ` + convExpr + ` ORDER BY timestamp DESC) AS rn,
		timestamp, source, model, agent_name, git_repo,
		` + uncachedInputSQL + ` AS uncached_input,
		` + outputTokensSQL + ` AS canonical_output,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost
	FROM generations WHERE ` + where + `
)
SELECT
	k,
	COUNT(*),
	COALESCE(SUM(uncached_input), 0),
	COALESCE(SUM(canonical_output), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0),
	COUNT(cost),
	COUNT(*) - COUNT(cost),
	SUM(cost),
	MIN(timestamp),
	MAX(timestamp),
	MAX(CASE WHEN rn = 1 THEN source END),
	MAX(CASE WHEN rn = 1 THEN model END),
	MAX(CASE WHEN rn = 1 THEN agent_name END),
	MAX(CASE WHEN rn = 1 THEN git_repo END),
	COUNT(*) OVER ()
FROM w
GROUP BY k ORDER BY MAX(timestamp) ` + string(dir) + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("conversations query: %w", err)
	}
	defer rows.Close()

	var out []ConversationSummary
	var total int64
	for rows.Next() {
		var c ConversationSummary
		var key sql.NullString
		var first, last int64
		var source, model, agent, repo sql.NullString
		var costTotal sql.NullFloat64
		if err := rows.Scan(
			&key,
			&c.Requests,
			&c.InputTokens,
			&c.OutputTokens,
			&c.CacheReadTokens,
			&c.CacheCreationTokens,
			&c.ReasoningTokens,
			&c.CostKnownCount,
			&c.CostUnknownCount,
			&costTotal,
			&first,
			&last,
			&source,
			&model,
			&agent,
			&repo,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("conversations scan: %w", err)
		}
		c.Key = key.String
		// Synthetic per-day keys carry the UTC day start; generic Other
		// collapses to Key "" while agent groups keep their prefixed key
		// so cards stay distinct and drill down to their own filter.
		switch {
		case hasKeyPrefix(c.Key, convKeyOtherPrefix):
			after, _ := cutPrefix(c.Key, convKeyOtherPrefix)
			c.Key = ""
			c.Day = dayString(after)
		case hasKeyPrefix(c.Key, convKeyAutocompletePrefix):
			after, _ := cutPrefix(c.Key, convKeyAutocompletePrefix)
			c.Day = dayString(after)
		case hasKeyPrefix(c.Key, convKeyTitleProgressPrefix):
			after, _ := cutPrefix(c.Key, convKeyTitleProgressPrefix)
			c.Day = dayString(after)
		}
		c.CostTotal = nullFloat(costTotal)
		c.FirstTimestamp = time.UnixMilli(first).UTC()
		c.LastTimestamp = time.UnixMilli(last).UTC()
		c.Source = source.String
		c.Model = model.String
		c.AgentName = agent.String
		c.GitRepo = repo.String
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("conversations rows: %w", err)
	}
	return out, total, nil
}

// TimeseriesSourcePoint is one bucket of one source's token totals.
type TimeseriesSourcePoint struct {
	BucketStart         int64
	Source              string
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
}

// TotalTokens is the sum of all five token columns.
func (p TimeseriesSourcePoint) TotalTokens() int64 {
	return p.InputTokens + p.OutputTokens + p.CacheReadTokens + p.CacheCreationTokens + p.ReasoningTokens
}

// TimeseriesBySource returns per-bucket token aggregates per source; hour
// and day buckets are grouped in SQL, week and month buckets merge day rows
// in Go like Timeseries.
func TimeseriesBySource(ctx context.Context, db *sql.DB, f Filter, bucket Bucket) ([]TimeseriesSourcePoint, error) {
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
	source,
	COUNT(*),
	COALESCE(SUM(` + uncachedInputSQL + `), 0),
	COALESCE(SUM(` + outputTokensSQL + `), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0)
FROM generations WHERE ` + where + ` GROUP BY 1, source ORDER BY 1`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("source timeseries query: %w", err)
	}
	defer rows.Close()

	var days []TimeseriesSourcePoint
	for rows.Next() {
		var p TimeseriesSourcePoint
		if err := rows.Scan(
			&p.BucketStart,
			&p.Source,
			&p.Requests,
			&p.InputTokens,
			&p.OutputTokens,
			&p.CacheReadTokens,
			&p.CacheCreationTokens,
			&p.ReasoningTokens,
		); err != nil {
			return nil, fmt.Errorf("source timeseries scan: %w", err)
		}
		days = append(days, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source timeseries rows: %w", err)
	}
	if bucket.directSQL() {
		return days, nil
	}
	return mergeSourceDays(days, bucket), nil
}

func mergeSourceDays(days []TimeseriesSourcePoint, bucket Bucket) []TimeseriesSourcePoint {
	var out []TimeseriesSourcePoint
	idx := map[string]int{} // bucketStart|source -> position in out
	for _, d := range days {
		start := bucketStart(d.BucketStart, bucket)
		k := fmt.Sprintf("%d\x00%s", start, d.Source)
		if i, ok := idx[k]; ok {
			out[i].Requests += d.Requests
			out[i].InputTokens += d.InputTokens
			out[i].OutputTokens += d.OutputTokens
			out[i].CacheReadTokens += d.CacheReadTokens
			out[i].CacheCreationTokens += d.CacheCreationTokens
			out[i].ReasoningTokens += d.ReasoningTokens
			continue
		}
		d.BucketStart = start
		idx[k] = len(out)
		out = append(out, d)
	}
	return out
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

// dayString renders a group-key day suffix (UTC millis, with a raw fallback
// for unexpected values) as YYYY-MM-DD.
func dayString(after string) string {
	if ms, err := strconv.ParseInt(after, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC().Format("2006-01-02")
	}
	return after
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
