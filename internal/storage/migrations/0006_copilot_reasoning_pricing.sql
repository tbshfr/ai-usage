-- Copilot's reported output already includes reasoning. Estimates made before
-- that accounting fix charged reasoning twice. Reprice only rows with a saved
-- rate snapshot so the worker can preserve the original pricing terms.
UPDATE generations
SET pricing_pending = 1
WHERE source = 'copilot'
  AND reasoning_tokens > 0
  AND cost_source IN ('openrouter', 'manual')
  AND cost IS NOT NULL
  AND pricing_snapshot_id IS NOT NULL;
