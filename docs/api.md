# JSON API

Served on the dashboard port (default `127.0.0.1:8080`). Same-origin
only — no CORS. When dashboard credentials are configured
(`AI_USAGE_DASHBOARD_USER`/`AI_USAGE_DASHBOARD_PASSWORD`), every
`/api/*` request requires a valid login session: unauthenticated calls
get `401` with `{"error":"unauthorized","status":401}` instead of data.
`GET /health` and `GET /ready` stay unauthenticated for probes.

All responses are `application/json`, UTF-8, lowerCamelCase. Nullable
numerics serialize as JSON `null` when unknown — never `0`. Cost is
passthrough only: it appears only when the source reported it (opencode),
never computed.

## Shared filter parameters

All list/aggregate endpoints accept:

| Parameter  | Meaning                                                      |
|------------|--------------------------------------------------------------|
| `from`     | RFC3339 or `YYYY-MM-DD` (date-only = UTC midnight); optional |
| `to`       | RFC3339 or `YYYY-MM-DD`; optional, defaults to now           |
| `source`   | exact match (`opencode`, `copilot`); optional                |
| `provider` | exact match on raw stored provider; optional                 |
| `model`    | exact match on raw stored model; optional                    |

Invalid values → `400` with `{"error":"...","status":400}`.

## Endpoints

### `GET /api/summary`

Totals for the filter range, plus the filter echo.

`/api/summary?from=2026-02-01&to=2026-03-01`

```json
{
  "filter": {"from":"2026-02-01T00:00:00Z","to":"2026-03-01T00:00:00Z","source":"","provider":"","model":""},
  "requests": 7,
  "inputTokens": 596,
  "outputTokens": 436,
  "cacheReadTokens": 400,
  "cacheCreationTokens": 21,
  "reasoningTokens": 0,
  "costKnownCount": 3,
  "costTotal": 0.9,
  "costUnknownCount": 4
}
```

### `GET /api/timeseries?bucket=day|week|month`

Per-bucket aggregates (default `bucket=day`; week buckets start Monday,
month buckets at the 1st; all UTC). `bucketStart` is RFC3339.

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
    "reasoningTokens": 0,
    "costKnownCount": 8,
    "costUnknownCount": 0,
    "costTotal": 2.85
  }
]
```

### `GET /api/providers`

Same row shape as `/api/sources`, grouped by raw provider value
(`github`, `anthropic`, …).

### `GET /api/models`

Same row shape, grouped by raw model value, ordered by total tokens
descending.

### `GET /api/generations?limit&offset`

Full records, newest first. `limit` defaults to 50, clamped to a max of
500; `offset` pages forward.

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
  "received": 120,
  "normalized": 118,
  "stored": 117,
  "deduplicated": 1,
  "rejected": 2,
  "normalizationErrors": 0,
  "ingestionErrors": 0
}
```

## Health probes

`GET /health` always returns `{"status":"ok"}`; `GET /ready` returns 200
`{"status":"ready"}` when the database answers, 503 otherwise.
