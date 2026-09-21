package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// Filter is the single filter type used by every query method. Zero From
// means no lower bound; zero To means now. Source/Provider/Model match raw
// stored values exactly ("" = all). Conversation matches conversation_id
// exactly, except for the sentinels below: "none" selects every session-less
// row (no conversation ID, or a session-less copilot agent),
// "autocomplete" selects copilot autocomplete, and "titleprogress" selects
// the copilot title/progress helpers. Agent matches are always scoped to
// source='copilot' because other sources use free-form agent names.
type Filter struct {
	From         time.Time
	To           time.Time
	Source       string
	Provider     string
	Model        string
	Conversation string
}

func (f Filter) normalize(now time.Time) (Filter, error) {
	out := f
	out.Source = strings.TrimSpace(out.Source)
	out.Provider = strings.TrimSpace(out.Provider)
	out.Model = strings.TrimSpace(out.Model)
	out.Conversation = strings.TrimSpace(out.Conversation)
	if out.To.IsZero() {
		out.To = now
	}
	if !out.From.IsZero() && out.To.Before(out.From) {
		return out, fmt.Errorf("invalid filter: to (%s) before from (%s)", out.To, out.From)
	}
	return out, nil
}

// whereSQL builds the WHERE clause for the filter over unix-millisecond
// timestamps.
func (f Filter) whereSQL() (string, []any) {
	conds := []string{}
	args := []any{}
	if !f.From.IsZero() {
		conds = append(conds, "timestamp >= ?")
		args = append(args, f.From.UnixMilli())
	}
	conds = append(conds, "timestamp < ?")
	args = append(args, f.To.UnixMilli())
	for _, col := range []struct{ name, val string }{
		{"source", f.Source},
		{"provider", f.Provider},
		{"model", f.Model},
	} {
		if col.val != "" {
			conds = append(conds, col.name+" = ?")
			args = append(args, col.val)
		}
	}
	switch {
	case f.Conversation == ConversationNone:
		conds = append(conds, "(conversation_id IS NULL OR (source = ? AND agent_name IN (?, ?, ?)))")
		args = append(args, normalize.SourceCopilot, normalize.AgentXtabProvider, normalize.AgentTitle, normalize.AgentProgressMessages)
	case f.Conversation == ConversationAutocomplete:
		conds = append(conds, "(source = ? AND agent_name = ?)")
		args = append(args, normalize.SourceCopilot, normalize.AgentXtabProvider)
	case f.Conversation == ConversationTitleProgress:
		conds = append(conds, "(source = ? AND agent_name IN (?, ?))")
		args = append(args, normalize.SourceCopilot, normalize.AgentTitle, normalize.AgentProgressMessages)
	case f.Conversation != "":
		conds = append(conds, "conversation_id = ? AND (source != ? OR COALESCE(agent_name, '') NOT IN (?, ?, ?))")
		args = append(args, f.Conversation, normalize.SourceCopilot, normalize.AgentXtabProvider, normalize.AgentTitle, normalize.AgentProgressMessages)
	}
	return strings.Join(conds, " AND "), args
}

// Conversation filter sentinels: none selects every session-less row (no
// conversation ID, or a session-less copilot agent); autocomplete and
// titleprogress select the per-day copilot agent groups behind the
// Autocomplete and Title/progress session cards.
const (
	ConversationNone          = "none"
	ConversationAutocomplete  = "autocomplete"
	ConversationTitleProgress = "titleprogress"
)

// InsertGeneration stores one record, returning true when a new row was
// inserted. On ID conflict it merges: NULL columns are filled from the new
// record and non-nil values are never overwritten with nil — retried OTLP
// batches reuse the same trace/span IDs, possibly with more attributes
// filled in (docs/telemetry.md D2, README rule 3). A newly reported harness
// cost supersedes an estimate; existing harness costs are preserved. A
// merge re-queues pricing only when it fills previously-NULL model or token
// columns, so a retried batch with no new information never re-enqueues an
// already-enriched row; every merge still bumps pricing_revision so
// enrichment racing a re-delivered record cannot land stale results.
func InsertGeneration(ctx context.Context, db *sql.DB, gen normalize.Generation) (bool, error) {
	return insertGeneration(ctx, db, gen)
}

// InsertGenerations stores a batch of records in one transaction, returning
// the number of newly inserted rows per source. Per-record semantics match
// InsertGeneration (dedup via ID conflict, merge on conflict). The batch is
// atomic: any error rolls back all of it, so a retried OTLP export replays
// the whole batch (dedup makes replays safe).
func InsertGenerations(ctx context.Context, db *sql.DB, gens []normalize.Generation) (map[string]int, error) {
	if len(gens) == 0 {
		return nil, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin batch insert: %w", err)
	}
	defer tx.Rollback() // no-op after a successful commit
	stored := make(map[string]int)
	for _, gen := range gens {
		inserted, err := insertGeneration(ctx, tx, gen)
		if err != nil {
			return nil, err
		}
		if inserted {
			stored[gen.Source]++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit batch insert: %w", err)
	}
	return stored, nil
}

// execQuerier covers *sql.DB and *sql.Tx, letting the insert helpers serve
// both the single-record and the batched path.
type execQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertGeneration(ctx context.Context, q execQuerier, gen normalize.Generation) (bool, error) {
	row := q.QueryRowContext(ctx, insertSQL, insertArgs(gen)...)
	var id string
	switch err := row.Scan(&id); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		// conflict → merge only the missing pieces
		if _, err := q.ExecContext(ctx, mergeSQL, mergeArgs(gen)...); err != nil {
			return false, fmt.Errorf("merge generation: %w", err)
		}
		return false, nil
	default:
		return false, fmt.Errorf("insert generation: %w", err)
	}
}

const insertSQL = `INSERT INTO generations (
	id, timestamp, source, service_name, provider, model,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	cost, conversation_id, trace_id, span_id, duration_ms,
	agent_name, git_repo, git_branch, created_at, cost_reported_by_harness, cost_source, pricing_pending
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING RETURNING id`

const mergeSQL = `UPDATE generations SET
	timestamp = MIN(timestamp, ?),
	provider = COALESCE(provider, ?),
	model = COALESCE(model, ?),
	input_tokens = COALESCE(input_tokens, ?),
	output_tokens = COALESCE(output_tokens, ?),
	cache_read_tokens = COALESCE(cache_read_tokens, ?),
	cache_creation_tokens = COALESCE(cache_creation_tokens, ?),
	reasoning_tokens = COALESCE(reasoning_tokens, ?),
	cost = CASE WHEN cost_reported_by_harness = 1 THEN cost ELSE COALESCE(?, cost) END,
	cost_source = CASE WHEN cost_reported_by_harness = 1 OR ? IS NOT NULL THEN 'harness' ELSE cost_source END,
	pricing_model_id = CASE WHEN ? IS NOT NULL THEN NULL ELSE pricing_model_id END,
	pricing_fetched_at = CASE WHEN ? IS NOT NULL THEN NULL ELSE pricing_fetched_at END,
	pricing_rates = CASE WHEN ? IS NOT NULL THEN NULL ELSE pricing_rates END,
	cost_reported_by_harness = CASE WHEN ? IS NOT NULL THEN 1 ELSE cost_reported_by_harness END,
	pricing_pending = CASE
		WHEN cost_reported_by_harness = 1 OR ? IS NOT NULL THEN 0
		WHEN (model IS NULL AND ? IS NOT NULL)
			OR (input_tokens IS NULL AND ? IS NOT NULL)
			OR (output_tokens IS NULL AND ? IS NOT NULL)
			OR (cache_read_tokens IS NULL AND ? IS NOT NULL)
			OR (cache_creation_tokens IS NULL AND ? IS NOT NULL)
			OR (reasoning_tokens IS NULL AND ? IS NOT NULL)
		THEN 1
		ELSE pricing_pending
	END,
	pricing_revision = pricing_revision + 1,
	conversation_id = COALESCE(conversation_id, ?),
	duration_ms = COALESCE(duration_ms, ?),
	agent_name = COALESCE(agent_name, ?),
	git_repo = COALESCE(git_repo, ?),
	git_branch = COALESCE(git_branch, ?)
WHERE id = ?`

func insertArgs(gen normalize.Generation) []any {
	return []any{
		gen.ID,
		gen.Timestamp.UnixMilli(),
		gen.Source,
		nullableString(gen.ServiceName),
		nullableString(gen.Provider),
		nullableString(gen.Model),
		nullableInt(gen.InputTokens),
		nullableInt(gen.OutputTokens),
		nullableInt(gen.CacheReadTokens),
		nullableInt(gen.CacheCreationTokens),
		nullableInt(gen.ReasoningTokens),
		nullableFloat(gen.Cost),
		nullableString(gen.ConversationID),
		nullableString(gen.TraceID),
		nullableString(gen.SpanID),
		nullableDuration(gen.Duration),
		nullableString(gen.AgentName),
		nullableString(gen.GitRepo),
		nullableString(gen.GitBranch),
		time.Now().UnixMilli(),
		gen.Cost != nil,
		harnessCostSource(gen.Cost),
		gen.Cost == nil,
	}
}

func mergeArgs(gen normalize.Generation) []any {
	return []any{
		gen.Timestamp.UnixMilli(),
		nullableString(gen.Provider),
		nullableString(gen.Model),
		nullableInt(gen.InputTokens),
		nullableInt(gen.OutputTokens),
		nullableInt(gen.CacheReadTokens),
		nullableInt(gen.CacheCreationTokens),
		nullableInt(gen.ReasoningTokens),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		nullableFloat(gen.Cost),
		// pricing_pending: does the merge fill a pricing-relevant column?
		nullableString(gen.Model),
		nullableInt(gen.InputTokens),
		nullableInt(gen.OutputTokens),
		nullableInt(gen.CacheReadTokens),
		nullableInt(gen.CacheCreationTokens),
		nullableInt(gen.ReasoningTokens),
		nullableString(gen.ConversationID),
		nullableDuration(gen.Duration),
		nullableString(gen.AgentName),
		nullableString(gen.GitRepo),
		nullableString(gen.GitBranch),
		gen.ID,
	}
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableDuration(d time.Duration) any {
	if d == 0 {
		return nil
	}
	return d.Milliseconds()
}

// Only raw normalized telemetry enters the insert path; estimates are applied separately.
func harnessCostSource(cost *float64) string {
	if cost != nil {
		return "harness"
	}
	return "unknown"
}
