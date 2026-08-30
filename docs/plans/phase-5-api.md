# Phase 5 — JSON API

Read `docs/plans/README.md` first. Prerequisite: Phase 4 merged (query layer).

## Goal

A small, filterable JSON API served on the dashboard port (`:8080`,
localhost-bound), suitable for the Phase 6 dashboard and for power users
(jq/curl). Private by default, no auth in v1.

## Steps

### 1. Server — `internal/api/server.go`

- `http.Server` mounted under the existing `internal/api` lifecycle from
  Phase 2; Go 1.22 mux method+pattern routing.
- `Content-Type: application/json` everywhere; errors as
  `{"error": "...", "status": 400}`; UTF-8.
- CORS: none (same-origin dashboard). No API keys in v1 — the server binds
  localhost; note in docs that exposing it to a network is the user's
  responsibility and requires a reverse proxy with auth.
- Access log at debug level only (method, path, status, duration — no
  bodies).

### 2. Endpoints

Query parameters shared by all list/aggregate endpoints:
`from`, `to` (RFC3339 or `YYYY-MM-DD`; date-only means UTC midnight),
`source`, `provider`, `model`. Parse once into `storage.Filter` via one
helper with unit tests (invalid → 400 with a helpful message).

| Endpoint | Returns |
|---|---|
| `GET /api/summary` | Phase 4 `Summary` struct + filter echo |
| `GET /api/timeseries?bucket=day\|week\|month` | []Bucket (bucket start as RFC3339, token sums, `costTotal` nullable, `costKnownCount`) |
| `GET /api/sources` | `BySource` rows (key, requests, token sums, nullable cost) |
| `GET /api/providers` | `ByProvider` rows |
| `GET /api/models` | `ByModel` rows |
| `GET /api/generations?limit&offset` | `RecentGenerations` (default limit 50, max 500, offset for paging) |
| `GET /api/generations/{id}` | Single generation incl. `cost: null` when unreported |

JSON field naming: lowerCamelCase; every nullable numeric stays a JSON
`null` when absent — **never 0** (this is the API contract the UI depends
on; test it explicitly).

`GET /api/stats` (bonus, cheap): the Phase 3 ingestion counters snapshot —
received/stored/deduplicated/rejected/errors. Useful for Phase 7 and for
users debugging "why is nothing showing up".

### 3. Tests — `internal/api/server_test.go`

- Seed DB via the Phase 4 seed helper; `httptest.Server` over the real
  router.
- Per endpoint: happy path with filters (assert a filter actually narrows
  results — e.g. `source=opencode` excludes copilot rows), bad filter → 400,
  unknown id → 404, pagination bounds (limit clamp, offset past end).
- Cost contract test: an endpoint filtered to copilot-only must serialize
  `costTotal: null` (string equality against the raw JSON body, not the
  decoded struct, to catch 0-coercion regressions).
- Empty DB: every endpoint returns valid zero-value responses, not errors.

## Acceptance criteria

- [ ] All endpoints implemented + tested, including filter parsing edge
      cases.
- [ ] `curl localhost:8080/api/summary` on a live dogfooding instance
      returns real numbers (verify manually once with the Phase 3 e2e data).
- [ ] JSON contract documented in the endpoint table style in
      `docs/api.md` (brief: one example response per endpoint).
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- No write endpoints. No auth/session framework. No OpenAPI generator — the
  brief `docs/api.md` table + examples is the documentation.
- No pagination framework — limit/offset on the one list endpoint is enough.

## Report back

Endpoint list with any deviations from the table, the raw-JSON cost-null
test proof, and `docs/api.md` summary.
