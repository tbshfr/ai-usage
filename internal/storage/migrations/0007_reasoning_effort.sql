-- Reasoning-effort setting reported per call (Claude Code `effort`, Codex
-- `model_reasoning_effort`). Existing rows stay NULL: the value was never
-- captured for them.
ALTER TABLE generations ADD COLUMN reasoning_effort TEXT;
