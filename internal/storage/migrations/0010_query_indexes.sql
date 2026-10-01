-- Every dashboard query bounds timestamp, so dimension indexes carry it as
-- a second column: a dimension filter then narrows the time range (and
-- serves ORDER BY timestamp / MIN(timestamp)) without query-planner
-- statistics, which this database never collects.
DROP INDEX idx_generations_source;
DROP INDEX idx_generations_provider;
DROP INDEX idx_generations_model;
DROP INDEX idx_generations_token_id;
CREATE INDEX idx_generations_source   ON generations (source, timestamp);
CREATE INDEX idx_generations_provider ON generations (provider, timestamp);
CREATE INDEX idx_generations_model    ON generations (model, timestamp);
CREATE INDEX idx_generations_token_id ON generations (token_id, timestamp);

-- Session drill-down filters on conversation_id.
CREATE INDEX idx_generations_conversation_id ON generations (conversation_id, timestamp);

-- No query looks up trace_id.
DROP INDEX idx_generations_trace_id;
