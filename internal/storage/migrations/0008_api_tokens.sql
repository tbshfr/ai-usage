-- Multiple OTLP bearer tokens, grouped by a free-form label (e.g. "work",
-- "private"). Only the sha256 of each token is stored; revoked tokens keep
-- their row so past usage stays attributed.
CREATE TABLE api_tokens (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    token_hash BLOB NOT NULL UNIQUE,     -- sha256(plaintext)
    hint TEXT NOT NULL,                  -- last characters, for display
    created_at INTEGER NOT NULL,         -- unix milliseconds
    last_used_at INTEGER,                -- unix milliseconds
    revoked_at INTEGER                   -- unix milliseconds, NULL while active
);

-- Token that authenticated the export. NULL for unauthenticated loopback
-- ingestion and rows stored before tokens were tracked.
ALTER TABLE generations ADD COLUMN token_id INTEGER;
CREATE INDEX idx_generations_token_id ON generations (token_id);
