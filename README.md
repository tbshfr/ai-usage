# ai-usage

A local dashboard for your AI usage. It receives OpenTelemetry telemetry
directly from **OpenCode** (via the community OTel plugin) and **VS Code
GitHub Copilot** (native OTel support), normalizes every LLM call into one
canonical record, and stores it in a single SQLite file — all inside one
Go binary. A small web dashboard and JSON API on `:8080` show requests,
token counts, and trends over time. Cost is displayed **only where the
source itself reports it** (OpenCode reports a USD estimate; Copilot
reports none) — this project has no pricing subsystem and never computes
cost.

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

| Flag           | Env var                   | Default                 | Meaning                                    |
|----------------|---------------------------|-------------------------|--------------------------------------------|
| `--http`       | `AI_USAGE_HTTP_ADDR`      | `:8080`                 | Dashboard + JSON API listen address (empty disables) |
| `--otlp-http`  | `AI_USAGE_OTLP_HTTP_ADDR` | *(disabled)*            | OTLP/HTTP listen address (starts only when set) |
| `--otlp-grpc`  | `AI_USAGE_OTLP_GRPC_ADDR` | *(disabled)*            | OTLP gRPC listen address (starts only when set) |
| `--data-dir`   | `AI_USAGE_DATA_DIR`       | OS user-data dir + `ai-usage` | Data directory                       |
| `--database`   | `AI_USAGE_DATABASE`       | `<data-dir>/usage.db`   | SQLite database path                       |
| `--log-level`  | `AI_USAGE_LOG_LEVEL`      | `info`                  | `debug`, `info`, `warn`, or `error`        |

Default data directory per OS:

- Linux: `~/.local/share/ai-usage` (honors `XDG_DATA_HOME`)
- macOS: `~/Library/Application Support/ai-usage`
- Windows: `%LOCALAPPDATA%\ai-usage`

The listen addresses default to all interfaces on your machine; use
`--http 127.0.0.1:8080` (and likewise for the OTLP ports) to restrict
access to localhost only. Passing an empty value to any listener flag
(e.g. `--otlp-http ""`) disables that listener entirely.

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
