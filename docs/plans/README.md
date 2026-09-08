# AI Usage Dashboard — Agent Execution Plans

This directory contains one detailed plan per implementation phase. Each phase is
designed to be executed by a coding agent working autonomously. Read this file
first — it contains shared context that the phase plans do not repeat.

## Project summary

A single self-contained Go binary (`ai-usage`) that:

1. Receives GenAI usage telemetry via OTLP (HTTP on `:4318`, gRPC on `:4317`)
   from three sources:
   - **OpenCode** (terminal AI agent) via the community plugin
     `@devtheops/opencode-plugin-otel`
   - **VS Code GitHub Copilot** via its native, built-in OTel support
   - **Codex CLI** via its native OTel log exporter
2. Normalizes usage to a canonical `Generation` record (GenAI semantic
   conventions).
3. Stores records in SQLite (WAL mode) under the OS user-data directory.
4. Serves a local web dashboard + JSON API on `:8080` (html/template + HTMX,
   all assets embedded).
5. Displays cost **only when the source reports it** (opencode reports USD
   cost; Copilot and Codex report none). There is **no pricing subsystem** and never
   will be — no price files, no manual price entry, no cost calculation.

One executable + one SQLite database. No external collector, no Node, no
Python, no Postgres, no Docker required at runtime.

## Tech stack (fixed — do not substitute)

| Concern            | Choice |
|--------------------|--------|
| Language           | Go 1.26+ (module `github.com/tbshfr/ai-usage` — adjust if repo remote differs; use whatever `go mod init` name Phase 1 created) |
| SQLite driver      | `modernc.org/sqlite` (pure Go — required for CGO-free cross-compilation) |
| OTLP parsing       | `go.opentelemetry.io/collector/pdata` (+ `pdata/pprofile` not needed); gRPC services from `go.opentelemetry.io/proto/otlp` |
| gRPC               | `google.golang.org/grpc` |
| HTTP               | stdlib `net/http` (Go 1.22+ mux patterns allowed) |
| Logging            | stdlib `log/slog`, JSON handler, quiet by default |
| Templates/static   | `html/template` + `embed`; HTMX (`htmx.org` vendored as a single committed JS file); charting via a single committed JS file (uPlot or Chart.js) |
| Config             | flags > env vars > defaults (see Phase 2) |
| Tests              | stdlib `testing` + `net/http/httptest`; fixtures under `testdata/` |

No frontend build step. Any third-party JS is a single file downloaded once and
committed (no npm, no CDN at runtime).

## Repository layout (target)

```
ai-usage/
├── cmd/ai-usage/main.go
├── internal/
│   ├── ingest/        # OTLP receivers, signal routing
│   ├── normalize/    # Generation canonicalization, per-source mapping
│   ├── storage/       # SQLite open/migrate, queries
│   ├── api/           # JSON API handlers
│   ├── web/           # dashboard handlers, templates
│   └── config/
├── web/              # embedded static assets (embedded via internal/web)
├── migrations/        # .sql files, embedded
├── testdata/
│   ├── opencode/     # sanitized real OTLP payloads (from Phase 1)
│   ├── copilot/
│   └── codex/
├── docs/
│   ├── plans/        # these plans
│   ├── telemetry.md  # written in Phase 1
│   └── source-setup.md
├── go.mod
└── README.md
```

## Canonical record (single source of truth)

```go
// internal/normalize/normalize.go
type Generation struct {
    ID                 string        // deterministic dedup key, see below
    Timestamp          time.Time     // span start time (UTC)
    Source             string        // "opencode" | "copilot" | "codex"
    ServiceName        string        // resource service.name attribute
    Provider           string        // gen_ai.provider.name (raw, e.g. "github")
    Model              string        // gen_ai.response.model, fallback gen_ai.request.model
    InputTokens        *int64        // gen_ai.usage.input_tokens
    OutputTokens       *int64        // gen_ai.usage.output_tokens
    CacheReadTokens    *int64        // gen_ai.usage.cache_read.input_tokens
    CacheCreationTokens *int64       // gen_ai.usage.cache_creation.input_tokens
    ReasoningTokens    *int64        // gen_ai.usage.reasoning.output_tokens (accept legacy gen_ai.usage.reasoning_tokens)
    Cost               *float64      // passthrough only; nil when source reports none
    ConversationID     string        // gen_ai.conversation.id / session id
    TraceID            string
    SpanID             string
    Duration           time.Duration // span end - start
    AgentName          string        // gen_ai.agent.name, when present
    GitRepo            string        // github.copilot.git.repository, when present
    GitBranch          string        // github.copilot.git.branch, when present
}
```

Rules:
- Nullable fields stay nil when telemetry lacks the value. Never coerce
  missing to zero. Zero is only stored when the source explicitly reported 0.
- `Cost` is never computed. It exists in the DB only if a span/log carried it
  (OpenCode's selected span cost signals).

## Telemetry reference (verified facts — do not re-research)

### VS Code Copilot (native OTel)

Enabled with `"github.copilot.chat.otel.enabled": true` in VS Code settings.
Default endpoint `http://localhost:4318` (OTLP HTTP), default wire protocol
`http/protobuf` but `http/json` also supported; `otlp-grpc` optional.
`captureContent` defaults to false — leave it false. Official docs:
https://code.visualstudio.com/docs/agents/guides/monitoring-agents

Signal choice: **per-LLM-call `chat` spans are the generation records.**

- `chat` span (gen_ai.operation.name = "chat"): one per LLM API call, carries
  gen_ai.usage.input_tokens, output_tokens, cache_read.input_tokens,
  cache_creation.input_tokens, reasoning.output_tokens (legacy alias:
  gen_ai.usage.reasoning_tokens), gen_ai.provider.name, gen_ai.request.model,
  gen_ai.response.model, gen_ai.response.finish_reasons, server.address,
  copilot_chat.time_to_first_token.
- `invoke_agent` span (gen_ai.operation.name = "invoke_agent"): **session
  aggregates — MUST NOT become generation records** (double counting).
- `execute_tool` / `execute_hook` spans: not usage records; ignore.
- Metrics (`gen_ai.client.token.usage`, `copilot_chat.*`) and events: ignore
  for generation records (histograms/counters, would double count).
- Resource attributes: service.name (`copilot-chat` or `github-copilot`),
  service.version, session.id.
- Extra metadata on spans: github.copilot.git.repository / .branch /
  .commit_sha, gen_ai.agent.name, gen_ai.conversation.id.
- **No cost attributes exist for Copilot.**

### OpenCode (via @devtheops/opencode-plugin-otel)

Plugin emits the same usage through three signals: traces (session/llm/tool
spans), metrics (opencode.token.usage, opencode.cost.usage — cumulative
counters), and log events (`api_request` with tokens/cost/duration,
`session.idle` with totals, `user_prompt`, …).

- Truth signal is decided in Phase 1 from captured fixtures (default
  expectation: `llm`/`chat` spans, or `api_request` log events).
- **Only one signal is ingested as generation records.** Metrics are never
  generation records.
- Token types: input, output, reasoning, cacheRead, cacheCreation.
- Cost: opencode computes USD cost itself (models.dev pricing) and the plugin
  forwards it. Store as passthrough; it is opencode's estimate, not ours.
- Exact attribute names for the chosen truth signal are documented in
  `docs/telemetry.md` after Phase 1 capture — **the Phase 1 document wins
  over anything written here if they disagree.**

## Deduplication rules

1. Deterministic ID per record:
   - Copilot: `sha256("copilot|" + traceID + "|" + spanID)`
   - opencode: `sha256("opencode|" + traceID + "|" + spanID)` (or
     `sessionID|messageID` if the truth signal is logs — decided in Phase 1)
   - Codex: content hash of conversation ID, full event timestamp, model, and
     the five nullable token buckets (its terminal logs have no trace/span IDs)
2. `INSERT ... ON CONFLICT(id) DO NOTHING` — retried OTLP batches must never
   produce duplicates.
3. One LLM request can span multiple OTLP exports (e.g. streaming spans with
   updated attributes): if a record with same ID already exists and the new
   one has strictly more information (e.g. output tokens now present), update
   (merge non-nil fields) instead of ignoring. Implement as
   upsert-if-newer/non-nil-merge only if fixtures show this case; otherwise
   plain DO NOTHING is acceptable.

## Global guardrails (apply to every phase)

- **Privacy**: never log prompt/completion content, raw telemetry, or token
  text. Raw payloads are stored in SQLite only behind a disabled-by-default
  option, and only for debugging — prefer not implementing raw storage at all
  unless a fixture proves it necessary. Default binds: localhost.
- **No pricing**: any PR introducing price tables, price files, or cost
  computation is wrong. `Cost` is passthrough only.
- **No over-engineering**: no plugin frameworks, no queues, no external
  services, no generic telemetry platform. Keep the loop:
  OTLP → normalize → SQLite → SQL → HTML/JSON.
- **No comments in code** unless a non-obvious decision needs 1–2 lines of
  explanation (e.g. why invoke_agent spans are skipped).
- Match existing code style in neighboring files. Idiomatic Go, table-driven
  tests where natural.
- Every phase ends with: `go build ./... && go vet ./... && go test ./...`
  passing, and `gofmt -l .` empty.

## Verification commands (run in every phase)

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .
```

## Phase sequence and prerequisites

| Phase | File | Depends on | Status |
|-------|------|------------|--------|
| 1 | `phase-1-telemetry-capture.md` | nothing (creates the Go module) | done |
| 2 | `phase-2-skeleton.md` | Phase 1 (module + fixtures exist) | done |
| 3 | `phase-3-ingestion.md` | Phase 2 (config, DB, receivers running) + Phase 1 (fixtures, truth-signal decision) | done |
| 4 | `phase-4-persistence.md` | Phase 3 (records being stored) | done |
| 5 | `phase-5-api.md` | Phase 4 | done |
| 6 | `phase-6-ui.md` | Phase 5 | done |
| 7 | `phase-7-hardening.md` | Phases 1–6 | done |
| 8 | `phase-8-distribution.md` | Phase 7 | done |
| 9 | `phase-9-auth.md` | Phases 1–8 | done |
| 10 | `phase-10-codex.md` | Phases 1–9 merged | done |
| 11 | [phase-11-backups.md](phase-11-backups.md) | Phases 1–10 merged | implemented; R2 smoke test pending |

Each phase plan is self-contained: an agent that has read this README plus its
phase file can execute it. Do not start a phase before its prerequisites are
merged.
