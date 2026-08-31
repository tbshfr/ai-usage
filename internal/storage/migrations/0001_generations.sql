CREATE TABLE generations (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,          -- unix milliseconds, UTC

    source TEXT NOT NULL,                -- "opencode" | "copilot"
    service_name TEXT,

    provider TEXT,                       -- raw gen_ai.provider.name
    model TEXT,                          -- gen_ai.response.model preferred

    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_creation_tokens INTEGER,
    reasoning_tokens INTEGER,

    cost REAL,                           -- passthrough, NULL when unreported

    conversation_id TEXT,
    trace_id TEXT,
    span_id TEXT,

    duration_ms INTEGER,

    agent_name TEXT,
    git_repo TEXT,
    git_branch TEXT,

    created_at INTEGER NOT NULL          -- unix milliseconds
);

CREATE INDEX idx_generations_timestamp ON generations (timestamp);
CREATE INDEX idx_generations_source   ON generations (source);
CREATE INDEX idx_generations_provider ON generations (provider);
CREATE INDEX idx_generations_model    ON generations (model);
CREATE INDEX idx_generations_trace_id ON generations (trace_id);
