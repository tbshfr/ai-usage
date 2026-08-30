# Phase 7 — Hardening, edge cases, and full test pass

Read `docs/plans/README.md` first. Prerequisite: Phases 1–6 merged.

## Goal

Make the system trustworthy under real conditions: retried/duplicate
telemetry, mixed encodings, restarts, malformed records, large batches, and
an audited guarantee that nothing sensitive is ever logged or exposed. This
phase is mostly tests plus small fixes they force.

## Work items

Each item = write the test first, fix if it fails, keep the test.

### 1. Duplicate & retry behavior

- e2e: POST the same fixture batch 3× back-to-back → exactly one row set.
- e2e: POST the same batch after app restart → still no duplicates (proves
  dedup is DB-backed, not in-memory).
- If the opencode truth signal can emit partial-then-complete records (per
  Phase 3's merge decision), test the merge path: partial first, complete
  second → final row has all fields; complete first, partial retry → row
  unchanged.

### 2. Encoding & transport matrix

- For every trace fixture: ingest via HTTP/protobuf, HTTP/JSON, and gRPC →
  byte-identical Generation rows (compare via query). Any divergence is a
  normalization bug (double-cast, etc.).
- Content-type edge cases: missing header → 400 per OTLP spec; gzip
  Content-Encoding (OTLP HTTP allows it — support via stdlib gzip reader;
  test with a gzipped fixture body).

### 3. Exclusion guarantees

- A copilot fixture containing `invoke_agent` + N `chat` spans yields exactly
  N generations, never N+1.
- An opencode metrics batch yields zero generations but nonzero `received`
  counters.
- The non-truth opencode signals (if present in fixtures) yield zero rows.

### 4. Malformed input resilience

- Batch with one corrupt span (bad types on gen_ai attributes — int where
  string expected, missing trace id, zero span id): batch succeeds, one
  record counted `normalization_errors`, receiver returns 200.
- Fully undecodable body → 400/INVALID_ARGUMENT, app keeps running; follow-up
  good batch still succeeds.
- Empty batch, empty resource spans, span with no attributes at all — no
  panics, correct counters.

### 5. Restart & persistence

- Integration test: start app (temp data dir) → ingest fixture → SIGTERM →
  verify clean shutdown logs → start again → assert rows intact and
  `schema_migrations` idempotent, `/ready` green, then ingest again → no
  dups.

### 6. Large batches

- Generate synthetically (in test) a 5,000-span trace batch (mostly non-chat
  spans) + 500 chat spans: completes within a generous timeout (e.g. <10s),
  memory stays bounded (no requirement to measure precisely; just no
  unbounded buffering — process record-by-record, never accumulate full
  decoded structs beyond the batch pdata already in memory).

### 7. Unknown models & sparse data

- Rows with model strings not matching any display-provider prefix →
  display falls back to raw model/provider; cost null; tokens shown.
- A generation with only `input_tokens` set: summary/timeseries treat output
  as absent (NULL sums), detail renders `—`.

### 8. Privacy & logging audit

- Grep-audit the codebase: no `slog` call includes span attributes, payloads,
  or token values — only counts/IDs/error strings. Add a review note in the
  PR.
- Ensure `--dump-dir` (Phase 1 capture flag) either still works as a debug
  feature with a loud log warning at startup, or was removed during Phase 3
  refactors — either is fine; state which in the report.
- Verify UI/API cannot leak prompt content structurally: no column stores
  it, no raw payload storage exists.

### 9. Operational endpoints

- If not already present: `GET /health`, `GET /ready` re-verified, plus
  `GET /api/stats` counters exposed (Phase 5). Optionally a minimal
  `GET /metrics` in Prometheus text format for the Phase 3/5 counters —
  only if trivial (`ai_usage_*` names from README §19); skip if it pulls in
  a dependency.

### 10. Final gate

```bash
go build ./... && go vet ./... && go test ./... -race && gofmt -l .
```

`-race` green. Optionally `-shuffle=on` once to catch order dependence.

## Acceptance criteria

- [ ] All tests above exist and pass, including `-race`.
- [ ] The restart e2e and duplicate-retry matrix are part of the committed
      suite.
- [ ] Privacy audit findings recorded (should be "clean").
- [ ] No behavior changes beyond what failing tests forced; list every fix
      in the report.

## Do NOT

- Do not add new features to fix a test if the test is wrong — re-read the
  relevant phase plan and README first; fixtures + telemetry.md are ground
  truth.
- No new dependencies beyond what's already used.

## Report back

List of tests added (grouped by the 10 items), every production fix made
with root cause, final gate output, and anything deferred with reasons.
