-- Per-reason breakdown of the daily ingestion counters. Reasons come from
-- a fixed enum (see internal/ingest/pipeline.go), so the table grows by at
-- most days x kinds x reasons rows; unauthenticated traffic can only bump
-- integer counts on existing-shape rows, never add rows.
CREATE TABLE stats_daily_reasons (
    day TEXT NOT NULL,                   -- UTC date, YYYY-MM-DD
    kind TEXT NOT NULL,                  -- rejected | ignored | norm_error | dedup | http_reject
    reason TEXT NOT NULL,                -- fixed enum value
    count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, kind, reason)
);
