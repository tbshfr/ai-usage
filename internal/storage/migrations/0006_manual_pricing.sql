-- Replace the column to extend its CHECK constraint while preserving all costs.
ALTER TABLE generations RENAME COLUMN cost_source TO legacy_cost_source;
ALTER TABLE generations ADD COLUMN cost_source TEXT NOT NULL DEFAULT 'unknown'
    CHECK (cost_source IN ('unknown', 'harness', 'openrouter', 'manual', 'free'));
UPDATE generations SET cost_source = legacy_cost_source;
ALTER TABLE generations DROP COLUMN legacy_cost_source;
