ALTER TABLE generations ADD COLUMN cost_reported_by_harness INTEGER NOT NULL DEFAULT 0 CHECK (cost_reported_by_harness IN (0, 1));
ALTER TABLE generations ADD COLUMN cost_source TEXT NOT NULL DEFAULT 'unknown' CHECK (cost_source IN ('unknown', 'harness', 'openrouter', 'manual', 'free'));
ALTER TABLE generations ADD COLUMN pricing_model_id TEXT;
ALTER TABLE generations ADD COLUMN pricing_fetched_at INTEGER;
-- Retain only rate sets actually used by estimates, once per distinct value.
CREATE TABLE pricing_snapshots (
    id INTEGER PRIMARY KEY,
    rates_json TEXT NOT NULL UNIQUE
);
ALTER TABLE generations ADD COLUMN pricing_snapshot_id INTEGER REFERENCES pricing_snapshots(id);
ALTER TABLE generations ADD COLUMN pricing_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE generations ADD COLUMN pricing_revision INTEGER NOT NULL DEFAULT 0;
UPDATE generations SET cost_reported_by_harness = 1, cost_source = 'harness', pricing_pending = 0 WHERE cost IS NOT NULL;
CREATE INDEX idx_generations_pricing_pending ON generations(id) WHERE pricing_pending = 1;
CREATE TABLE pricing_catalog (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    fetched_at INTEGER NOT NULL,
    models_json TEXT NOT NULL
);
CREATE TABLE pricing_jobs (
    name TEXT PRIMARY KEY,
    completed_at INTEGER NOT NULL
);
