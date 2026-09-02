CREATE TABLE stats_daily (
    day TEXT PRIMARY KEY,                -- UTC date, YYYY-MM-DD

    received INTEGER NOT NULL DEFAULT 0,
    normalized INTEGER NOT NULL DEFAULT 0,
    stored INTEGER NOT NULL DEFAULT 0,
    deduplicated INTEGER NOT NULL DEFAULT 0,
    rejected INTEGER NOT NULL DEFAULT 0,
    ignored_not_used INTEGER NOT NULL DEFAULT 0,
    normalization_errors INTEGER NOT NULL DEFAULT 0,
    ingestion_errors INTEGER NOT NULL DEFAULT 0,

    updated_at INTEGER NOT NULL          -- unix milliseconds
);
