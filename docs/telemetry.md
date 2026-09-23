# Telemetry ground truth

OpenCode and Copilot were captured 2026-08-30 with the Phase 1 capture harness (`cmd/ai-usage`,
OTLP/HTTP on `:4318`, raw batch dumps). This document is built from real
payloads; the committed fixtures under `testdata/` derive from the same
captures. Attribute names here are the authoritative input for Phase 3
normalizers. Where this document disagrees with `docs/plans/README.md`,
this document wins. Codex was captured separately on 2026-09-07 with CLI
v0.153.4 and the same OTLP/HTTP harness.

## 1. Sources & versions

| Source | Version evidence | Encoding observed | Endpoint paths |
|---|---|---|---|
| OpenCode (plugin `@devtheops/opencode-plugin-otel`, self-reports `service.version` 1.2.3; opencode `app.version` 1.5.1) | resource `service.name=opencode` | **always protobuf** (`application/x-protobuf`) | `/v1/traces`, `/v1/metrics`, `/v1/logs` |
| VS Code GitHub Copilot (copilot-chat extension 0.63.0; VS Code version not recorded) | resource `service.name=copilot-chat` | **always JSON** (`application/json`) | `/v1/traces`, `/v1/metrics`, `/v1/logs` |
| Codex CLI 0.153.4 (Rust OTel SDK 0.31.0) | resource `service.name=codex_cli_rs` | **JSON observed** (`application/json`) | `/v1/logs`, `/v1/traces`, `/v1/metrics` |
| Maki 0.5.6 | resource `service.name=maki`, `telemetry.sdk.name=maki-otel` | **protobuf observed** (`application/x-protobuf`) | `/v1/logs`, `/v1/metrics` |

Resource attributes:

- opencode: `service.name=opencode`, `service.version=1.2.3`,
  `app.version=1.5.1`, `os.type`, `host.arch`,
  `deployment.environment=production`
- copilot-chat: `service.name=copilot-chat`, `service.version=0.63.0`,
  `session.id` (VS Code session; concatenates a GUID and a Unix-ms timestamp)
- Codex: `service.name=codex_cli_rs`, `service.version=0.153.4`, `env`,
  `host.name`, and Rust OTel SDK metadata

## 2. Copilot payloads (traces are the truth signal)

### Span tree

One agent turn exports across multiple `traces` batches, all sharing one
`traceID`. Observed tree (fixture `traces-invoke-agent.json` +
`traces-chat-tools.json` came from the same trace):

```
invoke_agent "GitHub Copilot Chat"      (root; the agent turn)
├── chat gpt-5.6-luna                   (main LLM call)
├── execute_tool runSubagent
│   └── invoke_agent "Explore"          (sub-agent)
│       ├── chat claude-haiku-4.5       (sub-agent LLM call ×2)
│       └── execute_tool read_file ×N
```

Title generation exports a standalone root `chat gpt-4o-mini-*` span in its
own batch (own traceID) — a real usage record, not an aggregate.

Span types:

| Span | `gen_ai.operation.name` | Role |
|---|---|---|
| `chat <model>` | `chat` | **generation record** (one per LLM API call) |
| `invoke_agent <name>` | `invoke_agent` | session aggregate — MUST NOT become a generation record (no usage attributes; would double count) |
| `execute_tool <name>` | `execute_tool` | tool call; not a usage record — ignore |

### `chat` span attributes

| Attribute | Type | Notes |
|---|---|---|
| `gen_ai.provider.name` | Str | `"github"` |
| `gen_ai.request.model` | Str | e.g. `claude-haiku-4.5` |
| `gen_ai.response.model` | Str | e.g. `claude-haiku-4.5-20251001` |
| `gen_ai.response.id` | Str | |
| `gen_ai.response.finish_reasons` | Slice | e.g. `[stop]` |
| `gen_ai.usage.input_tokens` | Int | |
| `gen_ai.usage.output_tokens` | Int | |
| `gen_ai.usage.cache_read.input_tokens` | Int | present on most models; 0 for non-cached |
| `gen_ai.usage.cache_creation.input_tokens` | Int | present on Claude spans, absent on GPT spans |
| `gen_ai.usage.reasoning.output_tokens` | Int | new convention |
| `gen_ai.usage.reasoning_tokens` | Int | **legacy alias observed simultaneously** on the same spans |
| `gen_ai.request.stream` | Bool | |
| `gen_ai.request.temperature` / `top_p` / `max_tokens` | Int/Double | |
| `gen_ai.response.time_to_first_chunk` | Double | seconds |
| `copilot_chat.time_to_first_token` | Int | milliseconds |
| `copilot_chat.server_request_id` | Str | |
| `copilot_chat.copilot_usage_nano_aiu` | Int | proprietary usage unit; not a token count |
| `gen_ai.conversation.id` | Str | chat conversation GUID |
| `copilot_chat.session_id` / `.chat_session_id` / `.parent_chat_session_id` | Str | tool-call rounds get their own `chat_session_id` |
| `gen_ai.agent.name` | Str | only on sub-agent spans, e.g. `tool/runSubagent-Explore` |
| `github.copilot.git.branch` | Str | observed; `.repository`/`.commit_sha` not observed in this capture but expected per docs |
| `github.copilot.agent.type` | Str | |
| `error.type`, `exception.*` | | on failed tool-call spans |

Content-bearing attributes **are present despite `captureContent` defaulting
to false**: `gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.system_instructions`, `gen_ai.tool.definitions`,
`gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`,
`copilot_chat.user_request`, `copilot_chat.reasoning_content`, and
`span.status.message` on failed tools. Fixtures carry these keys redacted to
`"[REDACTED]"`. Ingestion must never store them.

No cost attribute exists anywhere in Copilot telemetry. No `server.address`
was observed.

### Metrics (ignore for generation records)

`copilot_chat.session.count` (sum), `gen_ai.client.operation.duration`
(histogram, seconds, dims incl. `gen_ai.provider.name=github`),
`gen_ai.client.token.usage` (histogram, dims `gen_ai.token.type` ∈
`input|output`), plus `copilot_chat.*` counters. Histograms are aggregates —
ingesting them would double count.

### Logs (ignore for generation records)

Per-call events linked by trace/span ID:
`gen_ai.client.inference.operation.details` (request/response model, finish
reasons, usage ints, temperature/max tokens), `copilot_chat.agent.turn`
(usage + `tool_call_count`), `copilot_chat.tool.call`. Redundant with `chat`
spans; spans win because they carry duration and stable IDs.

## 3. opencode payloads

### Signals compared

| Field | `opencode.llm` span | `api_request` log | metrics |
|---|---|---|---|
| per-LLM-call granularity | yes | yes | no (cumulative sums) |
| tokens: input/output | `llm.token_count.prompt` / `.completion` | `input_tokens` / `output_tokens` | `opencode.token.usage` dims `type=input/output/reasoning/cacheRead/cacheCreation` |
| reasoning | `llm.token_count.completion_details.reasoning` | `reasoning_tokens` | dim `type=reasoning` |
| cache read | `llm.token_count.prompt_details.cache_read` | `cache_read_tokens` | dim `type=cacheRead` |
| cache write | `llm.token_count.prompt_details.cache_write` | `cache_creation_tokens` | dim `type=cacheCreation` |
| cost USD | `llm.cost.total` (also `cost_usd`) | `cost_usd` | `opencode.cost.usage` counter |
| model | `llm.model_name` | `model` | dim `model` |
| provider | `llm.system`/`llm.provider` + `gen_ai.provider.name` | `provider` | dim `provider` |
| duration | span start/end (+ `duration_ms` attr) | `duration_ms` | histogram |
| stable IDs | traceID + spanID | **none** | session.id dim only |
| agent | `agent.name`, `agent.type` | `agent`, `agent.name` | dim `agent` |
| finish reason | `llm.finish_reason` (e.g. `stop`, `tool-calls`) | — | — |

The plugin uses **OpenInference** conventions, not GenAI; `gen_ai.provider.name`
is the only GenAI bridge attribute it sets. Traces contain exactly two span
shapes:

- `opencode.llm` (`openinference.span.kind=LLM`, Client) — one per LLM call,
  child of the session span.
- `opencode.session` (`openinference.span.kind=AGENT`, Internal) — session
  aggregate: `session.total_tokens`, `session.total_cost_usd`,
  `session.total_messages`. MUST NOT become a generation record.

Tool calls do **not** produce their own spans; a tool-using turn is just an
`opencode.llm` span with `llm.finish_reason=tool-calls`.

Content is captured by default on spans: `input.value`, `output.value`,
`llm.input_messages`, `llm.output_messages` (contrary to the plan's
assumption; `OPENCODE_CAPTURE_PROMPT_IN_LOGS` only affects logs). Never store
these.

Log events (`event.name`): `session.created`, `user_prompt` (only
`prompt_length` — privacy-clean), `api_request`, `session.idle` (aggregates).
No trace/span IDs on log records.

Metrics: `opencode.token.usage` (sum, dim `type`), `opencode.cost.usage`,
`opencode.session.count/message.count/cache.count/model.usage`,
`opencode.session.duration/session.token.total/session.cost.total`
(histograms), `opencode.lines_of_code.total` (gauge). Never generation
records.

## 4. Codex payloads (logs are the truth signal)

### Signals compared

| Field | `codex.sse_event` / `response.completed` log | `handle_responses` span | `session_task.turn` span / metrics |
|---|---|---|---|
| per-LLM-response granularity | **yes** (12 captured) | transport spans (1009 captured; 10 token-bearing) | per-turn aggregates (5 captured) |
| model + conversation | **both present** | absent on token-bearing spans | present on turn spans |
| all token buckets | **yes** | yes on some spans | yes, but aggregated |
| stable trace/span IDs | empty | present | present |
| chosen | **yes** | no | no |

The selected log has `event.name=codex.sse_event` and
`event.kind=response.completed`. Its mapping is:

| Generation field | Codex attribute / value |
|---|---|
| Timestamp | `event.timestamp` (RFC3339; OTLP record timestamp is zero in captures) |
| Source / ServiceName | `"codex"` / resource `service.name` |
| Model | `model` |
| Provider | absent → empty |
| InputTokens | `input_token_count` (Str observed) |
| OutputTokens | `output_token_count` (Str observed; raw value includes reasoning) |
| CacheReadTokens | `cached_token_count` (Int observed) |
| CacheCreationTokens | `cache_write_token_count` (Int observed) |
| ReasoningTokens | `reasoning_token_count` (Int observed; subset of output) |
| ConversationID | `conversation.id` |
| Cost / Duration / trace IDs | absent → nil / zero / empty |

Numeric parsing accepts compatible int/double/string encodings. Longer
cache/reasoning attribute spellings from an earlier draft are accepted as
fallbacks, but the names above are what CLI 0.153.4 actually emitted.

Codex uses OpenAI-style inclusive buckets. Canonical input is
`input - cached - cache_write`, clamped at zero. Canonical output is
`output - reasoning`, also clamped at zero. The captured row
`input=24276, cached=23296, output=132, reasoning=19` therefore becomes
`input=980, cache=23296, output=113, reasoning=19`; its canonical total is
24408, exactly the captured `tool_token_count`. Turn-span totals likewise
show that reasoning is already included in output. Raw database columns remain
as emitted; SQL aggregates and individual API/UI records apply the mirrors
`UncachedInput` and `NonReasoningOutput`.

No per-response cost exists. A provider name appears on a separate
conversation-start event, but correlating mutable session state would make
ingestion order-dependent, so the response row keeps provider empty. Metrics
are aggregate histograms and Codex spans are rejected as non-generation spans.

Other Codex log types are privacy-sensitive even with
`log_user_prompt=false`: tool results can contain `arguments` and `output`, and
identity metadata includes `user.email`, `user.account_id`, and `host.name`.
The normalizer only reads the whitelisted response metadata above. Fixture
sanitization additionally removes those fields plus paths and conversation,
thread, turn, and call identifiers.

## 5. Maki payloads (API request logs are the truth signal)

Maki 0.5.6 was captured on 2026-09-23. Each `maki.api_request` log is one
model call, with `session.id`, `event.sequence`, `timeUnixNano`, `model`,
`provider`, `input_tokens`, `output_tokens`, `cache_read_tokens`,
`cache_creation_tokens`, `cost_usd`, and `duration_ms`. The captured call
with 826 input tokens and 15,360 cache-read tokens confirms that input is
already the uncached bucket. No reasoning-token attribute was observed.
Positive `cost_usd` is Maki's estimate and is treated as harness-reported cost.
Maki also emits zero when its price table has no estimate; zero leaves cost
unknown so the dashboard's pricing catalog can supply one.

The log's trace/span IDs are empty. Dedup uses the session ID, record timestamp,
and `event.sequence`. Maki resets the sequence when telemetry initializes, so
the timestamp distinguishes calls after a session resumes. The OTLP timestamp
also supplies the generation time.
Maki's delta token and cost metrics are interval aggregates; other event
types describe prompts, tools, errors, or decisions. None of those become
generation rows. Resource `telemetry.sdk.name=maki-otel` identifies Maki
when `service_name` has been customized.

The captured Maki export has logs and metrics, but no traces. Its call event
supplies the same core usage fields as OpenCode's `opencode.llm` span (model,
provider, input/output/cache tokens, cost, duration, session). It does not
report OpenCode's reasoning-token and agent-name attributes or trace/span
IDs. Maki also emits tool and permission events and an active-time metric;
these are not generation rows.

## 6. Decisions

### D1 — opencode truth signal: `opencode.llm` spans

Evaluation against the criteria: per-LLM-call granularity (yes), all five
token types (yes), cost (yes), stable dedup IDs (yes — traceID+spanID;
`api_request` logs have none), arrives exactly once per call (yes). Metrics
are cumulative and would double count; `opencode.session` spans are
aggregates. **Ingest only `opencode.llm` spans; ignore session spans, logs,
and metrics.** Copilot truth signal is its `chat` spans, for the same
reasons. Codex uses its terminal `response.completed` log because the span and
metric alternatives either aggregate a turn or lack model/conversation
identity. Maki uses its `maki.api_request` event for per-call usage. Exactly
one authoritative signal is ingested per source.

### D2 — dedup keys

- Copilot: `sha256("copilot|" + traceID + "|" + spanID)`
- opencode: `sha256("opencode|" + traceID + "|" + spanID)`
- Codex: SHA-256 over length-prefixed source/conversation/full-precision UTC
  event timestamp/model fields plus explicit nil-or-value encodings for the
  five token buckets
- Maki: `sha256("maki|" + session.id + "|" + decimal timeUnixNano + ":" + decimal event.sequence)`

Trace/span IDs are stable across export batches: one Copilot agent turn was
exported in four separate `traces` batches reusing the same traceID and span
IDs, so `INSERT ... ON CONFLICT DO NOTHING` (with non-nil-merge upsert, see
`docs/plans/README.md` rule 3) deduplicates correctly.

Codex log records have empty trace/span IDs. Its content-derived key is stable
across exporter retries and distinguishes missing tokens from explicit zero.
Two genuinely separate responses with the same conversation, timestamp,
model, and all token values remain a residual collision risk because Codex
emits no response ID.

### D3 — attribute → `Generation` mapping

For the two span-based sources, `Timestamp` = span start (UTC), `Duration` = end − start,
`TraceID`/`SpanID` = OTLP IDs, missing values stay nil (never coerce to zero).

| Generation field | Copilot `chat` span | opencode `opencode.llm` span |
|---|---|---|
| ID | `sha256("copilot\|" + traceID + "\|" + spanID)` | `sha256("opencode\|" + traceID + "\|" + spanID)` |
| Source | `"copilot"` | `"opencode"` |
| ServiceName | resource `service.name` (`copilot-chat`) | resource `service.name` (`opencode`) |
| Provider | `gen_ai.provider.name` | `gen_ai.provider.name`, fallback `llm.system` |
| Model | `gen_ai.response.model`, fallback `gen_ai.request.model` | `llm.model_name` |
| InputTokens | `gen_ai.usage.input_tokens` | `llm.token_count.prompt` |
| OutputTokens | `gen_ai.usage.output_tokens` | `llm.token_count.completion` |
| CacheReadTokens | `gen_ai.usage.cache_read.input_tokens` | `llm.token_count.prompt_details.cache_read` |
| CacheCreationTokens | `gen_ai.usage.cache_creation.input_tokens` | `llm.token_count.prompt_details.cache_write` |
| ReasoningTokens | `gen_ai.usage.reasoning.output_tokens`, fallback legacy `gen_ai.usage.reasoning_tokens` | `llm.token_count.completion_details.reasoning` |
| Cost | **always nil** — no attribute exists | `llm.cost.total`, fallback `cost_usd` |
| ConversationID | `gen_ai.conversation.id` | `session.id` |
| AgentName | `gen_ai.agent.name` (absent on top-level chats) | `agent.name` |
| GitRepo | `github.copilot.git.repository` (not observed in capture) | absent |
| GitBranch | `github.copilot.git.branch` | absent |

Filter rules: ingest only spans with `gen_ai.operation.name=chat` (Copilot)
or `openinference.span.kind=LLM` / span name `opencode.llm` (opencode).
Everything else (`invoke_agent`, `execute_tool`, `opencode.session`, metrics,
and non-authoritative logs) is dropped before normalization. Codex and Maki
use the log events described above; Codex spans are rejected.

## 7. Open questions and resolved accounting choices

1. **Cache accounting anomaly**: several opencode spans report
   `cache_read` > `prompt` (e.g. prompt 136, cache_read 6912), confirming
   the plugin's prompt count excludes cached tokens while Copilot's
   (OpenAI-style) includes them. Default: store both as reported; do not
   attempt to reconcile at ingest. *Resolved for display:* every aggregate
   sums `storage.uncachedInputSQL` — Copilot/Codex input minus cache tokens
   (clamped at 0), OpenCode/Maki as stored — so `InputTokens` is the
   uncached prompt under one convention everywhere, `TotalTokens` no longer
   double-counts Copilot cache, and the cache hit rate
   (`storage.CacheHitRate`) is a single formula
   (`cache_read / (input + cache_read + cache_creation)`) that is exact for
   mixed aggregates too. Single records expose the same value via
   `normalize.Generation.UncachedInput`.
2. **Legacy + new reasoning attributes present simultaneously** with equal
   values in capture. Default: prefer `gen_ai.usage.reasoning.output_tokens`,
   fall back to the legacy alias; if both present they are equal, so either
   order is safe.
3. **Copilot cache_creation absent for GPT models.** Default: nil, not 0.
4. **Title-generation `chat` spans (gpt-4o-mini) are root spans** with no
   `invoke_agent` parent. Default: ingest them — they are real usage.
5. **`copilot_chat.copilot_usage_nano_aiu`** is a proprietary usage unit with
   no `Generation` field. Default: ignore.
6. **`github.copilot.git.repository` / `.commit_sha`** were not observed
   despite docs. Default: map if present, else nil.
7. **Content attributes are always present in real payloads** from both
   sources. Default: normalizers whitelist attributes; content keys are
   never read or stored.
8. **Copilot duration units**: `copilot_chat.time_to_first_token` is ms,
   `gen_ai.response.time_to_first_chunk` is seconds; span start/end is the
   only duration source for `Generation`.
9. **Codex reasoning is a subset of output.** Resolved for display: subtract
   reasoning from Codex output in every aggregate and single-record response,
   while preserving raw storage. OpenCode and Copilot remain passthrough.

## Fixture provenance

| Fixture | Source batch |
|---|---|
| `testdata/copilot/traces-chat-simple.json` | title-gen + first main `chat` (gpt-4o-mini root, gpt-5.6-luna) |
| `testdata/copilot/traces-chat-tools.json` | sub-agent `chat` ×2 + `execute_tool read_file` ×4 (Claude spans with legacy + new reasoning attrs) |
| `testdata/copilot/traces-legacy-reasoning.json` | `chat` + `execute_tool` with both reasoning variants and `cache_creation` |
| `testdata/copilot/traces-invoke-agent.json` | `invoke_agent` ×2 + root `chat` + `execute_tool runSubagent` + `github.copilot.git.branch` |
| `testdata/copilot/metrics.json` | one metrics batch (`copilot_chat` histograms/sums) |
| `testdata/copilot/logs.json` | events incl. `gen_ai.client.inference.operation.details` |
| `testdata/opencode/traces-llm.json` + `.pb` | plain single-turn (`cache_read=64`) |
| `testdata/opencode/traces-llm-nocache.json` | single turn, no cache |
| `testdata/opencode/traces-llm-multiturn.json` | multi-turn, `cache_read=3520`, `finish_reason=tool-calls` |
| `testdata/opencode/logs.json` | `session.created`, `user_prompt`, `api_request`, `session.idle` |
| `testdata/opencode/metrics.json` | one metrics batch (all `opencode.*` metrics) |
| `testdata/codex/logs-sse-events.json` | two real terminal response logs, including nonzero cache + reasoning |
| `testdata/codex/logs-other.json` | one `codex.user_prompt` and one `codex.tool_result`, sensitive values redacted |
| `testdata/codex/traces-codex.json` | one token-bearing `handle_responses` and one aggregate `session_task.turn` span |
| `testdata/codex/metrics-codex.json` | one `codex.turn.token_usage` metric |

Privacy audit: all fixtures redact content-bearing attributes and span status
messages to `"[REDACTED]"` (`cmd/sanitize`); the `.pb` fixture is a re-marshal
of the sanitized JSON. Decode tests (`internal/ingest/decode_test.go`) assert
both structure and redaction. Raw dumps were not committed.

## Implementation notes

- pdata v1.65.0 API: unmarshalers/marshalers are structs
  (`&ptrace.JSONUnmarshaler{}`, `ptrace.ProtoUnmarshaler{}`) with pointer
  receivers — no `New*Unmarshaler()` constructors. pdata's protobuf + JSON
  unmarshalers were sufficient; no fallback to raw
  `go.opentelemetry.io/proto/otlp` needed.
- Helper commands committed: `cmd/inspect` (pretty-print any OTLP file), 
  `cmd/sanitize` (redact content → OTLP/JSON to stdout).
