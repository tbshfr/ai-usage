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

-- Hashes replaced by a regenerate. They never authenticate again, but the
-- configured-token import still recognizes them, so regenerating the
-- imported token does not re-import the old value on the next start.
CREATE TABLE api_token_retired_hashes (
    token_hash BLOB PRIMARY KEY,
    token_id INTEGER NOT NULL,
    retired_at INTEGER NOT NULL          -- unix milliseconds
);

-- Token that authenticated the export. NULL for unauthenticated loopback
-- ingestion and rows stored before tokens were tracked.
ALTER TABLE generations ADD COLUMN token_id INTEGER;
CREATE INDEX idx_generations_token_id ON generations (token_id);
