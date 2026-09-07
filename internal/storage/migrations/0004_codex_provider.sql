-- Codex generations were stored with an empty provider because Codex
-- telemetry only reports the provider name on a separate conversation-start
-- event. The Codex CLI always talks to OpenAI, so backfill the stored rows
-- to match the normalizer (see internal/normalize/codex.go). Re-ingestion
-- only fixes rows that are actually replayed (via the merge path), so
-- backfill the rest here.
UPDATE generations SET provider = 'openai'
WHERE source = 'codex' AND (provider IS NULL OR provider = '');
