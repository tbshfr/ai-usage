# Phase 2 — Application skeleton: config, storage, lifecycle

Read `docs/plans/README.md` first. Prerequisite: Phase 1 merged (Go module,
fixtures, `docs/telemetry.md` with decisions D1–D3).

## Goal

Turn the capture harness into a real application skeleton:

- Configuration system (flags > env > defaults)
- SQLite database bootstrap with embedded migrations (WAL mode)
- Structured logging
- Graceful lifecycle for all servers (OTLP HTTP, dashboard placeholder,
  OTLP gRPC placeholder)
- `/health` and `/ready` endpoints
- Startup banner with the exact UX from the plan:

```text
AI Usage Dashboard

Dashboard: http://localhost:8080
OTLP HTTP: http://localhost:4318
OTLP gRPC: localhost:4317
Database:  ~/.local/share/ai-usage/usage.db
```

After this phase the app runs with zero configuration and creates its
database; telemetry is received but only counted, not yet stored (ingestion
pipeline comes in Phase 3).

## Steps

### 1. Config — `internal/config/config.go`

```go
type Config struct {
    HTTPAddr     string // dashboard + API
    OTLPHTTPAddr string
    OTLPGRPCAddr string
    DataDir      string // resolved OS user-data dir unless overridden
    DatabasePath string // <DataDir>/usage.db
}
```

Resolution rules:

| Flag                | Env var                       | Default                          |
|---------------------|-------------------------------|----------------------------------|
| `--http`            | `AI_USAGE_HTTP_ADDR`          | `:8080`                          |
| `--otlp-http`       | `AI_USAGE_OTLP_HTTP_ADDR`     | `:4318`                          |
| `--otlp-grpc`       | `AI_USAGE_OTLP_GRPC_ADDR`     | `:4317`                          |
| `--data-dir`        | `AI_USAGE_DATA_DIR`           | OS user-data dir (below)         |
| `--database`        | `AI_USAGE_DATABASE`           | `<DataDir>/usage.db`             |

- Flags override env vars; env overrides defaults.
- Empty `--otlp-grpc ""` (or env set to `""`) disables that listener — make
  each server individually optional the same way.
- OS user-data dir (`DataDir` default): Linux
  `$XDG_DATA_HOME|~/.local/share` + `/ai-usage`; macOS
  `~/Library/Application Support/ai-usage`; Windows
  `%LOCALAPPDATA%\ai-usage`. Use `os.UserConfigDir()`? No — that gives config
  dirs; implement a small helper honoring XDG on Linux and the documented
  paths elsewhere. Create the directory (`0755`) if missing.
- Write `config_test.go` covering: defaults, env override, flag-over-env
  precedence, disabled listener, default data-dir resolution per GOOS
  (unit-test via an injectable `homeDir`/`env` func — keep it simple).

### 2. Storage bootstrap — `internal/storage/sqlite.go`

- Open `DatabasePath` with `modernc.org/sqlite`; DSN options enabling WAL and
  busy timeout: `?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`
  (check modernc driver's pragma DSN syntax at the current version and use
  whatever it documents).
- `db.SetMaxOpenConns(1)` for writes; use a single `*sql.DB` (SQLite handles
  it fine with WAL) — note: if Phase 4 shows read contention, revisit with a
  separate read pool; do not pre-optimize now.
- Ping with retry (up to ~5s, 100ms backoff) so a cold-start disk hiccup
  doesn't kill the app.

### 3. Migrations — `internal/storage/migrations.go` + `migrations/`

- `migrations/0001_generations.sql` — the schema below, exactly.
- Embed with `//go:embed migrations/*.sql` from a package that can see the
  `migrations/` dir (either root-level `migrations.go` or move embed into
  `internal/storage` — pick one, keep it tidy).
- Migration mechanics: table `schema_migrations (version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL)`; each file runs in a transaction; `fs.Sub`
  ordering by numeric prefix; never edit an applied migration — new changes
  are new files.

```sql
CREATE TABLE generations (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,          -- unix milliseconds, UTC

    source TEXT NOT NULL,                -- "opencode" | "copilot"
    service_name TEXT,

    provider TEXT,                       -- raw gen_ai.provider.name
    model TEXT,                          -- gen_ai.response.model preferred

    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_creation_tokens INTEGER,
    reasoning_tokens INTEGER,

    cost REAL,                           -- passthrough, NULL when unreported

    conversation_id TEXT,
    trace_id TEXT,
    span_id TEXT,

    duration_ms INTEGER,

    agent_name TEXT,
    git_repo TEXT,
    git_branch TEXT,

    created_at INTEGER NOT NULL          -- unix milliseconds
);

CREATE INDEX idx_generations_timestamp ON generations (timestamp);
CREATE INDEX idx_generations_source   ON generations (source);
CREATE INDEX idx_generations_provider ON generations (provider);
CREATE INDEX idx_generations_model    ON generations (model);
CREATE INDEX idx_generations_trace_id ON generations (trace_id);
```

No cost columns beyond `cost`. No raw payload column (README guardrail).

- `storage_test.go`: open temp-dir DB → migrate → assert tables/indexes exist
  (`sqlite_master` query); migrate twice → no-op; fresh open on existing DB →
  idempotent.

### 4. Logging

`log/slog` JSON to stderr. Level `info` default, `--log-level` flag
(`debug|info|warn|error`). Events actually logged in this phase: startup
config summary, database opened, migration applied (version), listener
started, shutdown started/complete. Never log telemetry contents.

### 5. Wiring — `cmd/ai-usage/main.go`

- Compose: load config → open+ migrate DB → build slog logger → start
  listeners → block on signal.
- Keep the Phase 1 OTLP HTTP receiver running (it now logs per-batch counts
  at debug level: signal, encoding, record counts — numbers only).
- Add a placeholder `internal/api` server for the dashboard port with:
  - `GET /health` → `200 {"status":"ok"}` (liveness)
  - `GET /ready` → `200` iff DB ping succeeds, else `503`
- Graceful shutdown order: stop accepting (both HTTP servers via
  `context`+`Shutdown`, ~10s grace), then close DB. SIGINT and SIGTERM.
- Startup banner exactly as in the Goal (resolve `~` to the home dir in the
  printed path; print actual addresses honoring config).

### 6. gRPC placeholder

Register the OTLP gRPC server (`:4317`) now, even though Phase 3 fills in the
service implementations: set up the listener with
`google.golang.org/grpc` and log a clear "not implemented" return for the
three services (TraceService/MetricsService/LogsService from
`go.opentelemetry.io/proto/otlp`), OR defer the listener entirely if wiring
in a placeholder adds too much noise — either way Phase 3 must not have to
redo lifecycle code. State which option you took in the report.

## Acceptance criteria

- [ ] `./ai-usage` with no args starts, prints the banner, creates the DB
      with migrations applied, `/health` and `/ready` return 200.
- [ ] `curl -X POST localhost:4318/v1/traces` with a fixture body (either
      encoding) returns 200 and increments a debug counter.
- [ ] Ctrl-C shuts down both listeners cleanly (test manually and via a
      Go test that sends SIGTERM to itself or exercises the shutdown func).
- [ ] Config precedence, migrations, and data-dir tests pass.
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- No normalization or row inserts yet (Phase 3).
- No frontend work (Phase 6) beyond the health endpoints.
- Do not add config options that aren't in the table above (no retention, no
  raw-payload flags — not implementing raw storage at all).
- Do not use CGO or mattn/go-sqlite3.

## Report back

Config table as implemented, gRPC placeholder choice (registered now vs
deferred), migration list, test output, anything from Phase 1 fixtures that
conflicted with this schema (would go into a new migration decision, not an
edit of 0001).
