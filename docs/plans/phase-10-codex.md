# Phase 10 — Codex CLI source (log-based)

Read `docs/plans/README.md` first. Prerequisite: Phases 1–9 merged.
Status: implemented and verified 2026-09-07.
This phase adds a third source, **Codex CLI**, alongside opencode/copilot.
It intentionally breaks two README assumptions: "two sources" (now three)
and "no source's logs become generations" (Codex generations come from
logs — see D1 below).

## Goal

One `Generation` per Codex LLM API response, at the same per-call
granularity as Copilot `chat` spans and opencode `opencode.llm` spans, so
cross-source `requests` and per-request token charts stay comparable.

## Verified Codex facts (from `/tmp/ai-usage-capture/`, do not re-research)

- Emitter: Codex CLI (Rust OTel SDK), `service.name=codex_cli_rs`,
  v0.153.4, OTLP/HTTP `application/json` — the existing receivers accept
  it unchanged.
- Truth signal: log record `event.name=codex.sse_event` +
  `event.kind=response.completed` — one per LLM API response
  (12 occurrences across the captures). It carries `model`,
  `conversation.id`, `event.timestamp`, and token counts only — no prompt
  or completion text.
- Token attrs: `input_token_count`, `cached_token_count`,
  `cache_write_token_count`, `output_token_count`, `reasoning_token_count`.
  Convention is OpenAI-style like Copilot: `input` includes cached
  (e.g. `input=12561, cached=9984`). `tool_token_count` = input+output
  in every record — ignore it.
- `response.completed` records have empty `traceId`/`spanId` — dedup
  needs a synthetic key (D2 below).
- No provider attr on the record (`provider_name=OpenAI` appears only on
  `conversation_starts`); store `Provider=""` rather than introduce
  order-dependent cross-event state. No per-response cost is reported: nil.
- No duration (instant log) — store 0.
- Spans: `session_task.turn` is a per-turn aggregate (5 occurrences vs
  12 responses — ingesting it would undercount requests ~2.4x);
  `handle_responses` spans carry tokens but no model/conversation
  (1009 spans, ~10 with tokens). All codex spans are rejected, never
  stored. Metrics (`codex.turn.token_usage` etc.) are aggregates — ignored.
- Sensitivity (verified by scanning all captures): `codex.user_prompt`
  `prompt` arrives as `[REDACTED]` with prompt logging off (keep it off);
  `codex.tool_result` `arguments`/`output` carry real shell commands,
  workdirs, and tool stdout; `user.email` + `user.account_id` ride on
  nearly every log record. The mapping below whitelists token/model/
  conversation attrs only, so none of this is stored — but fixtures must
  be redacted and the sanitizer must cover these keys.

## Work items

### 1. `internal/normalize/normalize.go`

- Add `SourceCodex = "codex"`.
- `UncachedInput()`: treat `codex` like `copilot` (subtract read+write,
  clamp ≥0); update the comment (stored value stays as reported).
- Add `DetectLogSource(resource pcommon.Map) string`:
  `service.name=="codex_cli_rs"` → `SourceCodex`, else `""`.
- Add `FromLog(source string, resource pcommon.Map, lr plog.LogRecord)
  (Generation, bool, error)` dispatcher (switch on source; only codex
  exists today). Needs the `plog` import.

### 2. `internal/normalize/codex.go` (new) + `codex_test.go`

`FromCodexLog(resource, lr)`: proceed only for `codex.sse_event` +
`response.completed`, else `ok=false`. Require nonempty string `model` and
`conversation.id`; reject present-but-invalid token values. `Timestamp` comes
from `event.timestamp` (RFC3339Nano), falling back only to a nonzero record or
observed timestamp; never store the Unix epoch for a missing timestamp.
`Duration` 0; `TraceID`/`SpanID`/`AgentName`/`GitRepo`/
`GitBranch` empty; `Cost` nil; ID via `DedupLogID` (§3).

### 3. `internal/normalize/id.go`: `DedupLogID`

Hash length-prefixed identity strings plus canonical RFC3339Nano UTC time and
explicit nil/value token encodings. This is deterministic across exporter
retries, avoids delimiter ambiguity, distinguishes nil from zero, and retains
the residual identical-timestamp/model/payload collision risk.

### 4. Traces: codex spans are rejected, not stored

- `DetectSource`: add `codex_cli_rs` → `SourceCodex` plus span-attr
  fallback on the `codex.` prefix (mirrors the `github.copilot.` fallback).
- `FromSpan`: codex case → always `ok=false` with a 1–2 line comment
  (turn spans are aggregates, `handle_responses` lacks model/conversation).
  Effect: codex spans count as `rejected:not_a_generation`, like
  `invoke_agent`/`opencode.session` today, not `no_source`.

### 5. `internal/ingest/pipeline.go`: `ConsumeLogs` creates generations

Rework to mirror `ConsumeTraces`: iterate `resourceLogs`, per-resource
`DetectLogSource`, `FromLog` for codex records (same counters:
received/normalized, norm-error reasons, single `InsertGenerations`
txn, dedup accounting, one hub notify per stored batch). Non-codex and
non-signal records keep the current `ignored:logs` behavior. Update the
"no source's logs become generations" doc comment (no longer true).

### 6. Storage + web labels

- `internal/storage/queries.go`: `uncachedInputSQL` →
  `WHEN source IN ('copilot','codex')`; update the closed-world comment.
- `internal/web/format.go`: `friendlySource("codex") → "Codex"`.
- Codex reasoning is a subset of its output count. Preserve raw storage, but
  use `max(output-reasoning, 0)` for Codex in every SQL aggregate, model sort,
  API record, and web row/detail so totals never double-count reasoning.

### 7. Sanitizer + fixtures

- `cmd/sanitize` no longer exists (only `ai-usage`, `inspect`, `zzcdp`)
  though docs reference it — recreate per docs with the redaction list
  extended by Codex content, user/machine identity, path, and conversation/
  thread/turn/call ID keys. Remove duplicate sensitive keys before inserting
  one `[REDACTED]` value.
- `testdata/codex/` (empty today), each file <50KB, redacted:
  `logs-sse-events.json` (`response.completed` records incl. one with
  nonzero cached+reasoning), `logs-other.json` (redacted `user_prompt` +
  `tool_result`), `traces-codex.json` (`handle_responses` +
  `session_task.turn` → rejected), `metrics-codex.json` (→ ignored).

### 8. Tests

- `normalize/codex_test.go`: fixture-driven + synthetic — missing
  tokens stay nil, non-string `model` errors, non-sse records skipped,
  dedup determinism, full timestamp precision, and nil-vs-zero identity.
- `loadLogs` helper in `testhelpers_test.go`.
- `uncached_test.go` + `uncached_parity_test.go`: codex subtract/clamp
  cases, SQL↔Go parity.
- Ingest (`pipeline_test.go` / `e2e_test.go`): fixture logs JSON through
  the HTTP receiver → stored generation; other codex logs still
  `ignored:logs`; codex spans `rejected:not_a_generation`; retry POST
  dedups via `DedupLogID`.
- Storage: insert a codex row, assert uncached input + cache-hit rate.

### 9. Docs

- `docs/telemetry.md`: Codex section — signals-compared table (incl. why
  `session_task.turn` was rejected), D1 logs decision, D2 synthetic dedup
  key, D3 mapping, fixture provenance.
- `docs/source-setup.md`: Codex user-level `~/.codex/config.toml` `[otel]`
  snippet
  (endpoint `http://127.0.0.1:4318/v1/...`, `protocol="json"`, prompt
  redaction on).
- `docs/CAPTURE-INSTRUCTIONS.md`: Codex subsection. `README.md`: add
  codex to source lists.

## Verification

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .
```

Plus: decoded redaction tests prove every sensitive value is `[REDACTED]`, and
a raw-value scan finds no personal email, host, UUID, or path; manual run
`./ai-usage --otlp-http :4318`, one Codex prompt, dashboard shows a Codex generation with model
+ tokens and `requests` increments by the API-response count.

## Risks / open points

- Synthetic dedup key (§3) — no stable per-response ID exists upstream.
- Duration always 0 for codex rows.
- `cache_write` was 0 in all captures — mapped anyway.
- Codex telemetry is immature (naming wart `tool_token_count`,
  `thread.id` int/string dup on spans) — re-verify event/attr names on
  CLI upgrades.
