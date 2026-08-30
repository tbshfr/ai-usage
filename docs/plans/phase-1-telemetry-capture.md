# Phase 1 — Telemetry capture, fixtures, and ground-truth decisions

Read `docs/plans/README.md` first. This phase produces the Go module skeleton,
a minimal OTLP capture harness, real sanitized telemetry fixtures for both
sources, and the ground-truth document (`docs/telemetry.md`) that later phases
depend on.

**This phase involves a human-in-the-loop step** (generating real AI traffic
from opencode and VS Code Copilot). The plan marks it clearly `[HUMAN]`.
Everything else is agent work.

## Goal

1. A Go module and a runnable binary `ai-usage` with a *capture-only* OTLP
   receiver that accepts OTLP HTTP (protobuf **and** JSON) on `:4318` and
   writes every received batch to disk unchanged.
2. Sanitized fixtures under `testdata/opencode/` and `testdata/copilot/`.
3. `docs/telemetry.md` describing the real payloads, attribute-by-attribute.
4. Three written decisions (recorded in `docs/telemetry.md`, section
   "Decisions") that all later phases must follow:
   - D1: opencode truth signal (which of traces/logs/metrics becomes
     generation records)
   - D2: dedup keys per source
   - D3: attribute→`Generation` mapping tables per source

## Why capture first

No public fixture set exists for either source's current telemetry, and the
schema and normalizers must be built from real data, not guesses. Building the
capture harness first also proves the OTLP receiver works before the rest of
the app exists.

## Steps

### 1. Create the module and layout

```bash
go mod init github.com/tbshfr/ai-usage   # adjust name to the actual repo remote if set
mkdir -p cmd/ai-usage internal/ingest internal/normalize internal/storage \
         internal/api internal/web internal/config web/static web/templates \
         migrations testdata/opencode testdata/copilot docs
```

### 2. Dependencies

```
go get go.opentelemetry.io/collector/pdata@latest
go get modernc.org/sqlite@latest   # not used yet, but pins early for reproducibility
```

If `pdata` pulls a dependency tree that conflicts with stdlib usage, fall back
to `go.opentelemetry.io/proto/otlp` for protobuf decode plus
`pdata`'s JSON unmarshaler; document the choice in `docs/telemetry.md`
("Implementation notes"). Do not hand-roll protobuf parsing.

### 3. Capture harness

Create `cmd/ai-usage/main.go` (temporary, capture-only shape):

- Flags: `--otlp-http :4318`, `--dump-dir` (default: empty = do not dump;
  capture mode is explicitly opt-in).
- HTTP server with routes `POST /v1/traces`, `POST /v1/metrics`,
  `POST /v1/logs`.
- Content types to support on all three routes:
  - `application/x-protobuf` — decode with pdata's protobuf unmarshaler
    (per signal), re-encode deterministically.
  - `application/json` — decode with pdata's JSON unmarshaler.
  - Respond `200` with an empty protobuf/JSON body per OTLP spec. On decode
    failure respond `400` and log the error (message only, never the payload).
- Dump format: one file per request:
  `<dump-dir>/<signal>-<unixmilli>-<n>.<pb|json>` where `<signal>` ∈
  {traces, metrics, logs}, `<n>` is a monotonically increasing counter (pad to
  6 digits). Protobuf files store the *raw request body bytes*; JSON files
  store the raw body too. Also write a sidecar
  `<dump-dir>/index.jsonl` with one line per request:
  `{"ts":..., "signal":"traces", "encoding":"protobuf", "file":"...", "bytes":N,
  "resource_service_names":["opencode"], "span_count":12}` (extract service
  names + counts via pdata after decoding; this teaches you pdata usage early,
  which Phase 3 needs).
- Graceful shutdown on SIGINT/SIGTERM.

Keep it under ~200 lines. No config package yet, no DB. This harness is
scaffolding that Phase 3 will grow into the real receiver.

### 4. [HUMAN] Enable both sources and generate traffic

Write `docs/CAPTURE-INSTRUCTIONS.md` with these instructions, then ask the
user to perform them (this is the human-in-the-loop gate):

**OpenCode** — `~/.config/opencode/opencode.json`:

```jsonc
{
  "plugin": [["@devtheops/opencode-plugin-otel", {
    "enabled": true,
    "endpoint": "http://localhost:4318",
    "protocol": "http/protobuf"
  }]]
}
```

(Env-var alternative: `OPENCODE_ENABLE_TELEMETRY=1`,
`OPENCODE_OTLP_ENDPOINT=http://localhost:4318`,
`OPENCODE_OTLP_PROTOCOL=http/protobuf`. Prompts are NOT captured unless
`OPENCODE_CAPTURE_PROMPT_IN_LOGS` is set — do not set it.)

Then run `./ai-usage --dump-dir /tmp/ai-usage-capture/opencode` in one
terminal and use opencode normally: at least one session with (a) one plain
question, (b) one request that triggers a tool call, (c) one multi-turn
conversation so cache behavior shows up, (d) usage of the small model if
configured (title generation).

**VS Code** — `settings.json`:

```jsonc
{
  "github.copilot.chat.otel.enabled": true
  // endpoint already defaults to http://localhost:4318
  // captureContent stays false
}
```

Then run `./ai-usage --dump-dir /tmp/ai-usage-capture/copilot` and use agent
mode for: (a) one plain chat question, (b) one agent task that calls tools
and runs at least 2 LLM round-trips, (c) one longer multi-turn conversation.
Also test inline chat completion (tab completion) if available.

**Do not proceed past this step until the dump directories contain non-empty
files from both sources.** If the user cannot generate traffic now, stop and
report; do not fabricate fixtures.

### 5. Inspect and sanitize

For each dumped file:

- Decode with a small `go run ./cmd/inspect` tool you write (or a Go test
  helper) that pretty-prints: resource attributes, span/log/metric names, all
  attribute keys and value types.
- **Privacy audit before committing anything**: verify no prompt/completion
  content is present. With captureContent=false (Copilot) and prompt capture
  off (opencode plugin) there should be none — but `user_prompt` log events
  still contain `prompt_length` (fine) and possibly truncated prompts; check.
  If any content-bearing attribute is found, redact values in fixtures and
  note it in `docs/telemetry.md`.
- Sanitize: keep structure and attribute names/types real; values may be
  slightly perturbed if they could identify the user (repo URLs, file paths,
  org names). Token counts, durations, model names, IDs must be preserved as
  real — normalizers are built against them.
- Reduce to a minimal, diverse set and commit:
  - `testdata/copilot/traces-chat-simple.json` — one `invoke_agent` with one
    child `chat` span
  - `testdata/copilot/traces-chat-tools.json` — invoke_agent + ≥2 chat spans
    + execute_tool spans
  - `testdata/copilot/traces-legacy-reasoning.json` — only if the legacy
    `gen_ai.usage.reasoning_tokens` attribute appears; otherwise note its
    absence
  - `testdata/copilot/metrics.json` — one metrics batch (to prove we can
    decode, even though metrics are ignored downstream)
  - `testdata/copilot/logs.json` — only if Copilot sends log records at all
  - `testdata/opencode/<truth-signal-candidates>/…` — representative batches
    for **all three signals** (traces, metrics, logs), because D1 requires
    comparing them
  - Store fixtures as OTLP/JSON (pretty-printed). If a source only ever sends
    protobuf, additionally commit one raw `.pb` fixture for that source and a
    Go test that decodes it.

### 6. Write `docs/telemetry.md`

Structure:

1. **Sources & versions** — opencode plugin version, VS Code version used
   during capture, capture date.
2. **Copilot payloads** — for each fixture: span tree shape, full attribute
   table (key, type, example value, which spans carry it), resource
   attributes, observed encodings (protobuf/JSON), observed endpoint paths.
3. **opencode payloads** — same, per signal (traces/metrics/logs), plus a
   comparison table: which signal carries which fields (tokens, cost, model,
   provider, duration, IDs).
4. **Decisions**:
   - D1 truth signal for opencode with justification. Evaluation criteria in
     priority order: (a) has per-LLM-call granularity, (b) has all five token
     types, (c) has cost, (d) has stable IDs usable for dedup, (e) arrives
     exactly once per call. Expected winner: `llm`/`chat` spans or
     `api_request` log events; if both qualify, prefer the one with
     trace/span IDs.
   - D2 dedup key per source (see README rules; confirm trace/span IDs are
     stable across retries — retried OTLP exports must reuse IDs).
   - D3 mapping tables: `Generation` field ← source attribute, per source,
     including nullability notes (e.g. Copilot cache_creation absent for
     non-Anthropic models).
5. **Open questions** for Phase 3 (e.g. multi-variant attributes, weird
   model names) — each must have a proposed default handling so Phase 3 is
   never blocked.

### 7. Fixture-based smoke test

Add `internal/ingest/decode_test.go` (or similar): table-driven tests that
decode every committed fixture with pdata and assert high-level invariants
(e.g. "copilot traces-chat-simple.json yields exactly 1 chat span", "all
five token attributes are ints or absent"). This pins fixtures and gives
Phase 3 a head start.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.
- [ ] `testdata/copilot/` contains ≥3 trace fixtures + 1 metrics fixture
      captured from a real VS Code, sanitized.
- [ ] `testdata/opencode/` contains fixtures for all three signals captured
      from a real opencode run, sanitized.
- [ ] No fixture contains prompt/completion content (audited, stated in
      telemetry.md).
- [ ] `docs/telemetry.md` exists with sections 1–5, including D1/D2/D3.
- [ ] Fixture decode tests pass and are committed.

## Do NOT

- Do not design the SQLite schema yet (Phase 2/4).
- Do not implement normalization beyond what the decode tests assert.
- Do not commit raw dump directories — only curated, sanitized fixtures.
- Do not fabricate telemetry. Every fixture must originate from the capture
  step. If a documented attribute doesn't appear in capture (e.g. legacy
  reasoning alias), record its absence rather than inventing it.

## Report back

When done, report: fixtures committed (list), D1/D2/D3 decisions in one
sentence each, any open questions from telemetry.md, and test output.
