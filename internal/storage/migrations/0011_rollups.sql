-- Per-UTC-day rollups of generations for aggregate queries over long ranges
-- (see internal/storage/rollup.go). Token and cost columns hold the same
-- canonical values the aggregate queries sum over raw rows. Missing
-- provider/model/token are stored as ''/''/0 so they can be part of a key.
-- Triggers keep the rollups in sync and are (re)created from Go together
-- with a backfill, so the rollups always match the query expressions in code.

CREATE TABLE usage_daily (
    day INTEGER NOT NULL,                -- unix milliseconds, UTC day start
    source TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    token_id INTEGER NOT NULL,
    helper INTEGER NOT NULL,             -- 1 = copilot autocomplete/title/progress

    requests INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    cache_read_tokens INTEGER NOT NULL,
    cache_creation_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cost_reported INTEGER NOT NULL,
    cost_estimated INTEGER NOT NULL,
    cost_free INTEGER NOT NULL,
    cost_known INTEGER NOT NULL,
    cost_sum REAL NOT NULL,

    PRIMARY KEY (day, source, provider, model, token_id, helper)
) WITHOUT ROWID;

-- Rows split by the filterable dimensions so filters still apply; a group
-- spanning several days or models is merged at query time.
CREATE TABLE conversation_daily (
    day INTEGER NOT NULL,                -- unix milliseconds, UTC day start
    conversation TEXT NOT NULL,          -- Conversations group key
    source TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    token_id INTEGER NOT NULL,

    requests INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    cache_read_tokens INTEGER NOT NULL,
    cache_creation_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cost_reported INTEGER NOT NULL,
    cost_estimated INTEGER NOT NULL,
    cost_free INTEGER NOT NULL,
    cost_known INTEGER NOT NULL,
    cost_sum REAL NOT NULL,
    first_ts INTEGER NOT NULL,           -- unix milliseconds
    last_ts INTEGER NOT NULL,            -- unix milliseconds

    PRIMARY KEY (day, conversation, source, provider, model, token_id)
) WITHOUT ROWID;

-- Hash of the trigger/backfill definition the rollups were built with; a
-- mismatch at startup rebuilds them.
CREATE TABLE rollup_state (
    name TEXT PRIMARY KEY,
    definition_hash TEXT NOT NULL
);
