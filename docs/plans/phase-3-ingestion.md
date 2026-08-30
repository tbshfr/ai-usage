# Phase 3 — Ingestion, normalization, deduplication

Read `docs/plans/README.md` first. Prerequisites: Phase 2 (config, DB,
lifecycle) and Phase 1 (fixtures + `docs/telemetry.md` decisions D1/D2/D3).
**The mapping tables in `docs/telemetry.md` override anything in this file
where they disagree.**

## Goal

Complete the receive → normalize → dedup → persist pipeline:

- OTLP HTTP receiver (protobuf + JSON) and OTLP gRPC receiver, both feeding
  one pipeline
- Per-source normalization into `Generation` (README canonical struct)
- Deterministic dedup keys with `ON CONFLICT` semantics
- Ingestion statistics counters
- Per-record error resilience (one bad record never drops a batch)
- The `INSERT` path into the `generations` table

After this phase, real telemetry from both tools lands in SQLite exactly once
per LLM call, and the app is dogfoodable (inspect rows with sqlite queries).

## Steps

### 1. Signal routing — `internal/ingest/`

- Define an internal interface both receivers call:
  `ConsumeTraces(ctx, pdata.Traces) error`, `ConsumeLogs(ctx, pdata.Logs)`,
  `ConsumeMetrics(ctx, pdata.Metrics)` (metrics: counted, never stored).
- HTTP receiver (upgrade Phase 1 harness): decode per content-type, call the
  consumers, reply per OTLP spec (`200` empty body, `400` on malformed).
- gRPC receiver: implement `TraceServiceServer`, `MetricsServiceServer`,
  `LogsServiceServer` from `go.opentelemetry.io/proto/otlp`; convert proto →
  pdata (`pdataotlp` importers) and call the same consumers. Wire into the
  Phase 2 lifecycle.
- Batch error handling: a decode failure rejects the batch (400 /
  gRPC INVALID_ARGUMENT); a normalization failure inside the pipeline is
  per-record (below) and does not fail the batch.

### 2. Normalization core — `internal/normalize/`

`normalize.go`: the `Generation` struct (README) plus helpers:
`attrString`, `attrInt` (handles pdata Int/Double/String coercion — some
exporters send numbers as doubles), `attrDouble`.

`copilot.go`:
- Input: one `pdata.Span`. Only spans where
  `gen_ai.operation.name == "chat"` produce a Generation. Spans with
  `invoke_agent`, `execute_tool`, `execute_hook` (and anything else) are
  skipped — this is the double-counting guard; leave one explanatory code
  comment.
- Source detection: resource `service.name` in {`copilot-chat`,
  `github-copilot`} → `"copilot"`; span attributes may include
  `github.copilot.*` → also `"copilot"`. Define the detection order in code
  so it is testable.
- Map per `docs/telemetry.md` D3 (expected: input/output/cache_read/
  cache_creation/reasoning tokens with the legacy `gen_ai.usage.reasoning_tokens`
  alias fallback; provider from `gen_ai.provider.name`; model from
  `gen_ai.response.model` falling back to `gen_ai.request.model`;
  conversation id; TTFT *not* stored in v1 — it isn't in the canonical
  struct).
- Cost: always nil for copilot.
- Duration: span end − start. Timestamp: span start, UTC.

`opencode.go`:
- Implement for the truth signal chosen in D1 (expected: LLM spans or
  `api_request` log records — code both behind the decided one; do not
  ingest a second signal).
- Tokens per D3 (expected attribute names: check fixtures — plugin traces
  mirror GenAI semconv: `gen_ai.usage.*`; plugin logs carry
  `input`/`output`/`reasoning`/`cacheRead`/`cacheCreation`-style keys —
  fixtures decide exact names).
- Cost: extract the USD cost attribute present in the truth signal
  (fixture-verified name). Passthrough only.
- ID for dedup per D2.

Also `provider.go` (or in normalize.go): a small, pure display helper
`UnderlyingProvider(model string) string` deriving a friendly provider label
from the model id prefix (`claude-*` → anthropic, `gpt-*`/`o3*`/`o4*` →
openai, `gemini-*` → google, `grok-*` → xai, else `github`/unknown). Used
only for UI display in later phases — always store raw `provider` unchanged.

### 3. Dedup keys — `internal/normalize/id.go`

- `copilot`: `id = sha256hex("copilot|" + traceID + "|" + spanID)`
- `opencode`: per D2 (expected `sha256hex("opencode|" + traceID + "|" + spanID)`
  for spans; for logs `sha256hex("opencode|" + sessionID + "|" + messageID)`
  — fixtures decide).
- Same trace may contain multiple `chat` spans (multi-round agents): each is
  its own Generation with its own ID — verify with the
  `traces-chat-tools` fixture (assert ≥2 distinct chat-span IDs).

### 4. Persistence — `internal/storage/usage.go`

- `InsertGeneration(ctx, gen Generation) (inserted bool, err error)` —
  `INSERT ... ON CONFLICT(id) DO NOTHING`. Use `INSERT OR IGNORE` semantics
  only if the upsert-merge below is not needed.
- If Phase 1 fixtures showed retried/partial-then-complete exports for the
  same record (D2/D3 notes), implement merge-upsert instead: on conflict,
  fill only NULL columns from the new record when the new record is
  later-by-timestamp or has strictly more non-nil fields; never overwrite
  non-nil with nil. Otherwise plain DO NOTHING. State which was implemented
  and why in the report.
- Parameterized SQL only. Timestamps as unix milliseconds.

### 5. Pipeline + statistics — `internal/ingest/pipeline.go`

Receive batch → iterate resource spans / log records → for each record:
  1. detect source (resource attributes; unknown source → count and skip)
  2. normalize → on error: increment `normalization_errors`, log at debug
     (event name + error only), continue
  3. insert → count inserted vs deduplicated
Counters (in-memory, atomic, exported for Phase 5/6 via a small interface):
`received`, `normalized`, `stored`, `deduplicated`, `rejected`,
`normalization_errors`, `ingestion_errors`. Expose a `Stats()` snapshot.
These power the Phase 7 metrics endpoints.

A `context` timeout per batch (e.g. 10s) guards slow DB writes; on DB error
the batch returns error (receivers translate to 503/UNAVAILABLE so exporters
retry — dedup makes retry safe).

### 6. Tests

- `normalize/copilot_test.go`: table-driven over every copilot fixture:
  correct token fields (incl. legacy reasoning alias case if fixture exists),
  provider/model mapping, invoke_agent/execute_tool spans yield nothing,
  cost always nil.
- `normalize/opencode_test.go`: same for the opencode truth signal; cost
  passthrough non-nil; all five token types when fixture has them.
- `normalize/id_test.go`: stability (same input → same ID), distinct chat
  spans in one trace → distinct IDs.
- `storage/usage_test.go`: insert twice → second is deduplicated; merge
  behavior (if implemented) fills NULLs only.
- `ingest/e2e_test.go`: start the full HTTP receiver on an ephemeral port,
  POST every committed fixture (both encodings where possible) via
  `httptest`/real listener, assert: row counts per source, no duplicates,
  metrics batches produce zero rows, stats counters consistent
  (received == stored + deduplicated + rejected + normalization_errors).
- gRPC test: minimal — one trace fixture via grpc client (protoc-generated
  stubs already exist in `go.opentelemetry.io/proto/otlp`) through the
  receiver into the DB.

## Acceptance criteria

- [ ] Fixture-driven tests pass for both sources and both encodings.
- [ ] e2e test proves: fixture OTLP → rows in SQLite → duplicate POST →
      same row count.
- [ ] A malformed span inside an otherwise valid batch: batch succeeds, one
      record rejected, counted.
- [ ] gRPC ingestion works for traces.
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- Do not ingest `invoke_agent` spans, metrics, or the non-truth opencode
  signals as generations. Metrics consumers exist only for counting.
- Do not compute cost. Do not round or reinterpret token counts.
- Do not coerce absent attributes to zero.
- Do not store raw payloads.

## Report back

Truth signal + dedup keys implemented (one line), merge-upsert vs
DO-NOTHING decision and fixture evidence, any fixture attributes that
didn't match `docs/telemetry.md` (update that doc accordingly in this PR),
e2e test output.
