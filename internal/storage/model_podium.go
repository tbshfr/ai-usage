package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// ModelRankMetric selects how the Trends podium ranks models.
type ModelRankMetric string

const (
	RankTokens ModelRankMetric = "tokens"
	RankDays   ModelRankMetric = "days"
	RankCost   ModelRankMetric = "cost"
)

// ModelRank is one model's usage in the selected period. ActiveDays counts
// distinct UTC dates with at least one request. CostTotal is nil when none
// of the model's requests have known costs.
type ModelRank struct {
	Model              string
	TotalTokens        int64
	ActiveDays         int64
	CostTotal          *float64
	CostKnownCount     int64
	CostEstimatedCount int64
}

// TopModels returns the first three models for a metric and filter. OpenRouter
// creator prefixes use the same grouping rule as ByModel. The days ranking
// omits Copilot autocomplete/title/progress requests using their agent tags.
// Cost ranking omits models with no known costs.
func TopModels(ctx context.Context, db *sql.DB, f Filter, metric ModelRankMetric) ([]ModelRank, error) {
	var order string
	switch metric {
	case RankTokens:
		order = "total_tokens DESC, model ASC"
	case RankDays:
		order = "active_days DESC, total_tokens DESC, model ASC"
	case RankCost:
		order = "cost_total DESC, total_tokens DESC, model ASC"
	default:
		return nil, fmt.Errorf("invalid model rank metric %q", metric)
	}
	f, err := f.normalize(time.Now())
	if err != nil {
		return nil, err
	}
	where, args := f.whereSQL()
	if metric == RankDays {
		where += ` AND (source != ? OR COALESCE(agent_name, '') NOT IN (?, ?, ?))`
		args = append(args, normalize.SourceCopilot, normalize.AgentXtabProvider,
			normalize.AgentTitle, normalize.AgentProgressMessages)
	}
	dayCountSQL := fmt.Sprintf("COUNT(DISTINCT timestamp / %d)", dayMs)
	q := `WITH ranked AS (
		SELECT COALESCE(` + modelGroupKeySQL + `, '') AS model,
			` + totalTokensSumSQL + ` AS total_tokens,
			` + dayCountSQL + ` AS active_days,
			SUM(cost) AS cost_total,
			COUNT(cost) AS cost_known_count,
			COALESCE(SUM(cost_source IN ('openrouter', 'manual') AND cost IS NOT NULL), 0) AS cost_estimated_count
		FROM generations WHERE ` + where + ` AND COALESCE(model, '') <> ''
		GROUP BY ` + modelGroupKeySQL + `
	)
	SELECT model, total_tokens, active_days, cost_total, cost_known_count, cost_estimated_count
	FROM ranked`
	if metric == RankCost {
		q += ` WHERE cost_known_count > 0`
	}
	q += ` ORDER BY ` + order + ` LIMIT 3`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("top models query: %w", err)
	}
	defer rows.Close()
	var out []ModelRank
	for rows.Next() {
		var m ModelRank
		var cost sql.NullFloat64
		if err := rows.Scan(&m.Model, &m.TotalTokens, &m.ActiveDays, &cost, &m.CostKnownCount, &m.CostEstimatedCount); err != nil {
			return nil, fmt.Errorf("top models scan: %w", err)
		}
		if cost.Valid {
			m.CostTotal = &cost.Float64
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("top models rows: %w", err)
	}
	return out, nil
}
