# ai-usage

A local dashboard for your AI usage. It receives OpenTelemetry telemetry
directly from **OpenCode** (via the community OTel plugin) and **VS Code
GitHub Copilot** (native OTel support), normalizes every LLM call into one
canonical record, and stores it in a single SQLite file — all inside one
Go binary. A small web dashboard and JSON API on `:8080` show today's
token usage up front with weekly/monthly/all-time totals beside it (each
with the cache hit rate; click a card for details), a **Trends** page with
charts, per-source/provider/model breakdowns, and a **Sessions** view that
groups requests by conversation with a sortable request list. Cost is
displayed **only where the source itself reports it** (OpenCode reports a
USD estimate; Copilot reports none) — this project has no pricing
subsystem and never computes cost.

```
OpenCode / VS Code Copilot ──OTLP──▶ ai-usage ──▶ SQLite ──▶ dashboard + JSON API
                                      :4318 (HTTP) / :4317 (gRPC)    :8080
```

## Quickstart

1. Download the binary for your OS/arch from
   [releases](https://github.com/tbshfr/ai-usage/releases) (or
   [build from source](#build-from-source)).
2. Run it with the OTLP listener enabled:

   ```bash
   ./ai-usage --otlp-http :4318
   ```

   You should see:

   ```text
   AI Usage Dashboard (v0.1.0)

   Dashboard: http://localhost:8080
   OTLP HTTP: http://localhost:4318
   OTLP gRPC: (disabled)
   Auth:      off
   Database:  ~/.local/share/ai-usage/usage.db
   ```

   Only the dashboard starts by default; each OTLP listener starts only
   when its flag (or env var) is set.

3. Point OpenCode and/or VS Code Copilot at it — copy-paste configs are in
   [`docs/source-setup.md`](docs/source-setup.md).
4. Open <http://localhost:8080> and use your AI tools for a few minutes;
   requests appear as the tools report them.

## Build from source

Requires Go ≥ 1.26. No CGO, no Node, no other toolchain.

```bash
go build ./cmd/ai-usage
```

Cross-compiled release builds for linux/darwin/windows are produced by
[`scripts/release.sh`](scripts/release.sh) (outputs to `dist/` with
SHA-256 checksums).

## Configuration

Flags override environment variables, which override defaults.

| Flag                | Env var                       | Default                 | Meaning                                    |
|---------------------|-------------------------------|-------------------------|--------------------------------------------|
| `--http`            | `AI_USAGE_HTTP_ADDR`          | `127.0.0.1:8080`        | Dashboard + JSON API listen address (empty disables) |
| `--otlp-http`       | `AI_USAGE_OTLP_HTTP_ADDR`     | *(disabled)*            | OTLP/HTTP listen address (starts only when set) |
| `--otlp-grpc`       | `AI_USAGE_OTLP_GRPC_ADDR`     | *(disabled)*            | OTLP gRPC listen address (starts only when set) |
| `--data-dir`        | `AI_USAGE_DATA_DIR`           | OS user-data dir + `ai-usage` | Data directory                       |
| `--database`        | `AI_USAGE_DATABASE`           | `<data-dir>/usage.db`   | SQLite database path                       |
| `--log-level`       | `AI_USAGE_LOG_LEVEL`          | `info`                  | `debug`, `info`, `warn`, or `error`        |
| `--dashboard-user`  | `AI_USAGE_DASHBOARD_USER`     | *(auth off)*            | Dashboard login username                   |
| `--dashboard-password` | `AI_USAGE_DASHBOARD_PASSWORD` | *(auth off)*         | Dashboard login password                   |
| `--otlp-token`      | `AI_USAGE_OTLP_TOKEN`         | *(auth off)*            | Bearer token OTLP clients must send        |

Default data directory per OS:

- Linux: `~/.local/share/ai-usage` (honors `XDG_DATA_HOME`)
- macOS: `~/Library/Application Support/ai-usage`
- Windows: `%LOCALAPPDATA%\ai-usage`

The dashboard defaults to loopback so nothing is exposed beyond your
machine; use `--http 127.0.0.1:8080` (and likewise for the OTLP ports)
to restrict access to localhost only. Passing an empty value to any
listener flag (e.g. `--otlp-http ""`) disables that listener entirely.

## Authentication

Unauthenticated (no credential flags/env vars set), the dashboard and
any enabled OTLP listener are only allowed to bind to loopback
addresses. Binding to a non-loopback interface — `:8080`, `0.0.0.0`,
a public IP or hostname — **refuses to start** until you provide the
matching credentials. That makes the VPS setup safe by construction.

- **Dashboard**: a server-rendered login page. Set
  `AI_USAGE_DASHBOARD_USER` and `AI_USAGE_DASHBOARD_PASSWORD` (both
  required together). Sessions are HMAC-signed `HttpOnly` cookies valid
  for 7 days; restarting the process logs everyone out.
- **OTLP**: clients must send `Authorization: Bearer <token>` where the
  token comes from `AI_USAGE_OTLP_TOKEN`. Applies to both OTLP/HTTP and
  OTLP/gRPC. Client-side configuration is in
  [`docs/source-setup.md`](docs/source-setup.md).

Minimal VPS configuration (put the values in a systemd unit's
`Environment=` lines or an env file — do not commit them):

```bash
export AI_USAGE_HTTP_ADDR=":8080"
export AI_USAGE_OTLP_HTTP_ADDR=":4318"
export AI_USAGE_DASHBOARD_USER=admin
export AI_USAGE_DASHBOARD_PASSWORD='...long random password...'
export AI_USAGE_OTLP_TOKEN='...long random token...'
./ai-usage
```

`/health` and `/ready` stay unauthenticated so load balancers and
process supervisors can probe them. The comparison of credentials is
constant-time, and credentials never appear in logs.

Run the dashboard behind a TLS-terminating reverse proxy (nginx, Caddy)
when exposing it beyond a trusted network; the binary itself serves
plain HTTP.

## Data location & privacy

Everything stays on your machine. The binary makes no outbound network
connections; it only listens for OTLP and dashboard requests on the
addresses configured above.

What is collected: **metadata and token counts only** — timestamps,
source (opencode/copilot), provider, model, input/output/reasoning/cache
token counts, duration, conversation/trace IDs, and (from OpenCode) the
cost the source itself reports.

What is **not** collected: your prompts and completions. No prompt or
completion content is captured, stored, or logged. Raw telemetry payloads
are never persisted; logs are structured JSON containing no telemetry
data. To delete your history, stop the app and remove `usage.db` (and its
`-wal`/`-shm` companions) from the data directory.

## Development

- Project structure and phase plans: [`docs/plans/README.md`](docs/plans/README.md)
- Telemetry ground truth (what each source actually sends): [`docs/telemetry.md`](docs/telemetry.md)
- JSON API reference: [`docs/api.md`](docs/api.md)
- Source setup configs: [`docs/source-setup.md`](docs/source-setup.md)
- Fixture capture/refresh process: [`docs/CAPTURE-INSTRUCTIONS.md`](docs/CAPTURE-INSTRUCTIONS.md)

Verify changes:

```bash
go build ./... && go vet ./... && go test ./... && gofmt -l .
```
