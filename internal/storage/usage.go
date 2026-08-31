package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// InsertGeneration stores one record, returning true when a new row was
// inserted. On ID conflict it merges: NULL columns are filled from the new
// record and non-nil values are never overwritten with nil — retried OTLP
// batches reuse the same trace/span IDs, possibly with more attributes
// filled in (docs/telemetry.md D2, README rule 3).
func InsertGeneration(ctx context.Context, db *sql.DB, gen normalize.Generation) (bool, error) {
	row := db.QueryRowContext(ctx, insertSQL, insertArgs(gen)...)
	var id string
	switch err := row.Scan(&id); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		// conflict → merge only the missing pieces
		if _, err := db.ExecContext(ctx, mergeSQL, mergeArgs(gen)...); err != nil {
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
	agent_name, git_repo, git_branch, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	cost = COALESCE(cost, ?),
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
