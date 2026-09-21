# JSON API

Served on the dashboard port (default `127.0.0.1:8080`). Same-origin
only — no CORS. When dashboard credentials are configured
(`AI_USAGE_DASHBOARD_USER`/`AI_USAGE_DASHBOARD_PASSWORD`), every
`/api/*` request requires a valid login session: unauthenticated calls
get `401` with `{"error":"unauthorized","status":401}` instead of data.
`GET /health` and `GET /ready` stay unauthenticated for probes.

All responses are `application/json`, UTF-8, lowerCamelCase. Nullable
numerics serialize as JSON `null` when unknown — never `0`. Cost is
passthrough only: it appears only when the source reported it (OpenCode),
never computed.

## Shared filter parameters

All list/aggregate endpoints accept:

| Parameter     | Meaning                                                                      |
|---------------|------------------------------------------------------------------------------|
| `from`        | RFC3339 or `YYYY-MM-DD` (date-only = UTC midnight); optional                 |
| `to`          | RFC3339 or `YYYY-MM-DD`; optional, defaults to now                           |
| `source`      | exact match (`opencode`, `copilot`, `codex`); optional                       |
| `provider`    | exact match on raw stored provider; optional                                 |
| `model`       | exact match on raw stored model; optional                                    |
| `conversation`| exact match on conversation/session ID, or one of the session-less sentinels: `none` (every session-less row), `autocomplete` (VS Code autocomplete), `titleprogress` (title/progress helpers); optional |

Invalid values → `400` with `{"error":"...","status":400}`.

## Endpoints

### `GET /api/summary`

Totals for the filter range, plus the filter echo.

`/api/summary?from=2026-02-01&to=2026-03-01`

```json
{
  "filter": {"from":"2026-02-01T00:00:00Z","to":"2026-03-01T00:00:00Z","source":"","provider":"","model":"","conversation":""},
  "requests": 7,
  "inputTokens": 196,
  "outputTokens": 436,
  "cacheReadTokens": 400,
  "cacheCreationTokens": 21,
  "cacheHitRate": 0.65,
  "reasoningTokens": 0,
  "costKnownCount": 3,
  "costTotal": 0.9,
  "costUnknownCount": 4
}
```

`inputTokens` is the canonical uncached prompt: Copilot and Codex report
prompt input including cached tokens, so cached parts are subtracted before
aggregation (clamped at 0); OpenCode is stored uncached already. Codex also
reports reasoning as a subset of output; `outputTokens` excludes that subset
so `outputTokens + reasoningTokens` never double-counts it. Raw reported
values remain unchanged in SQLite.

`cacheHitRate` is the fraction of prompt tokens served from cache:
`cacheReadTokens / (inputTokens + cacheReadTokens + cacheCreationTokens)`.
The full prompt has the same shape under every source's convention, so the
rate is exact for mixed-source aggregates too. It is `null` when no prompt
tokens were reported in range.

### `GET /api/timeseries?bucket=hour|day|week|month`

Per-bucket aggregates (default `bucket=day`; hour buckets at the hour,
week buckets start Monday, month buckets at the 1st; all UTC).
`bucketStart` is RFC3339.

`/api/timeseries?bucket=month`

```json
[
  {
    "bucketStart": "2026-03-01T00:00:00Z",
    "requests": 7,
    "inputTokens": 144,
    "outputTokens": 178,
    "cacheReadTokens": 10,
    "cacheCreationTokens": 19,
    "reasoningTokens": 5,
    "costKnownCount": 2,
    "costTotal": 1.1
  }
]
```

### `GET /api/sources`

One row per source (request/token totals, nullable cost).

```json
[
  {
    "key": "opencode",
    "requests": 8,
    "inputTokens": 92,
    "outputTokens": 184,
    "cacheReadTokens": 0,
    "cacheCreationTokens": 56,
    "cacheHitRate": null,
    "reasoningTokens": 0,
    "costKnownCount": 8,
    "costUnknownCount": 0,
    "costTotal": 2.85
  }
]
```

`cacheHitRate` follows the same rule as on `/api/summary`.

### `GET /api/providers`

Same row shape as `/api/sources`, grouped by raw provider value
(`github`, `anthropic`, …).

### `GET /api/models`

Same row shape, grouped by model and ordered by total tokens descending.
For OpenRouter rows, the creator prefix before the first slash is omitted
from the group key, so `z-ai/glm-5.3-flash` and `glm-5.3-flash` share one row.
Stored model values and the `model` filter remain exact and unchanged.

### `GET /api/generations?limit&offset&order`

Full records ordered by timestamp. `limit` defaults to 50, clamped to a
max of 500; `offset` pages forward; `order` is `desc` (default, newest
first) or `asc` (oldest first). `inputTokens` is the canonical uncached
prompt (same rule as the aggregates); the raw as-reported value stays in
the database.

`/api/generations?limit=1`

```json
[
  {
    "id": "b3f9…",
    "timestamp": "2026-03-02T00:15:00Z",
    "source": "opencode",
    "serviceName": "opencode",
    "provider": "anthropic",
    "model": "claude-haiku-4-5-20251001",
    "inputTokens": 15,
    "outputTokens": 30,
    "cacheReadTokens": null,
    "cacheCreationTokens": 10,
    "reasoningTokens": null,
    "cost": 0.6,
    "conversationId": "ses_…",
    "traceId": "…",
    "spanId": "…",
    "durationMs": 4210,
    "agentName": "",
    "gitRepo": "",
    "gitBranch": ""
  }
]
```

### `GET /api/generations/{id}`

One record, same shape as list rows. `cost` is `null` when the source did
not report it. Unknown id → `404`.

### `GET /api/stats`

Ingestion counters since process start — useful when "nothing shows up".

```json
{
  "received": 165,
  "normalized": 118,
  "stored": 117,
  "deduplicated": 1,
  "rejected": 2,
  "ignoredNotUsed": 45,
  "normalizationErrors": 0,
  "ingestionErrors": 0
}
```

`rejected` counts spans that were detected as belonging to a source but
could not become a generation (e.g. non-`chat` Copilot spans), or spans from
an unknown source. `ignoredNotUsed` counts non-terminal/unsupported log records
and metric datapoints. Codex `response.completed` logs are the exception: they
are generation records. Both counters are part of the invariant
`received == normalized + rejected + ignoredNotUsed + normalizationErrors`.

### `GET /api/stats/reasons?day=YYYY-MM-DD`

The per-reason breakdown behind the day rows on the `/stats` page: fixed
`kind`/`reason` pairs (no free-form values), so the response is always a
small bounded list. Today's row serves the live counters; older days serve
persisted rows.

```json
[
  { "kind": "rejected", "reason": "no_source", "count": 4 },
  { "kind": "http_reject", "reason": "unauthorized", "count": 9 }
]
```

Kinds: `rejected` (spans that can never become records), `ignored` (unused log
records and metric datapoints), `norm_error` (malformed generation spans/logs),
`dedup` (duplicate records, broken down by source), and `http_reject`
(requests rejected before the pipeline — auth failures, malformed
requests — counted per request, not per record). Today's row serves the
live counters; older days serve persisted rows. A day with nothing
recorded returns `[]`; a malformed `day` returns `400`.

### `GET /api/backup`

Returns the current backup worker status (with the same authentication as other
API routes):

```json
{"status":"failed","enabled":true,"running":false,"last_success":"2026-09-08T12:00:00Z","failed_at":"2026-09-09T12:00:00Z","failure_stage":"upload"}
```

`status` is `disabled`, `pending` (waiting for the first backup), `running`,
`ok`, or `failed`. A failure remains `failed` during retries (`running: true`)
until success. Unset timestamps are `null`; `failure_stage` is empty without a
failure. `last_success` is the successful completion time restored from local
state; failures are tracked only for the current process. No credentials or raw
errors are returned. This endpoint returns 200 even when a backup failed: alert
on `status: failed` and on the age of `last_success`. `ok` does not guarantee
freshness or verify that the remote object still exists.

## Health probes

`GET /health` always returns HTTP 200 with `{"status":"ok","backup":"healthy"}`.
The `backup` field is `disabled` when unconfigured, `unhealthy` after a failed
attempt (including during retries), and `healthy` otherwise. Waiting for or
running the first backup is `healthy`; the health summary never reports
`pending` or `running`. A successful backup clears an outstanding failure. It summarizes worker state, not backup freshness; use
`/api/backup` for timestamps and details. This summary is public like the probe.

`GET /ready` returns 200
`{"status":"ready"}` when the database answers, 503 otherwise.
Backup failures do not affect either probe's HTTP status; monitor the `backup`
field or `/api/backup` separately.
