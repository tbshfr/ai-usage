# ai-usage

`ai-usage` is a self-hosted dashboard for AI coding-tool usage. It receives
OpenTelemetry data from OpenCode, VS Code GitHub Copilot, Codex CLI, and Maki,
normalizes each model call, and stores the result in SQLite.

The dashboard shows token use, cache hit rates, costs, trends, model and provider
breakdowns, and requests grouped by session. The web UI, JSON API, OTLP
receivers, database migrations, and static assets ship in one Go binary.

## Quick start

1. Download a binary from [GitHub Releases](https://github.com/tbshfr/ai-usage/releases)
   or [build from source](#build-from-source).
2. Start the OTLP/HTTP receiver:

   ```sh
   ./ai-usage --otlp-http 127.0.0.1:4318
   ```

3. Configure one or more [supported clients](docs/client-setup.md).
4. Open <http://localhost:8080> and use the client. New requests appear after
   the client exports its telemetry.

Only the dashboard starts by default. OTLP/HTTP and OTLP/gRPC listeners start
when you configure their address.

## Supported clients

| Client | Telemetry used | Cost handling |
|---|---|---|
| OpenCode | Per-call spans from `@devtheops/opencode-plugin-otel` | Uses the reported cost when present |
| VS Code GitHub Copilot | Native per-call chat spans | Estimates missing costs when pricing is available |
| Codex CLI | Native terminal response logs | Estimates missing costs when pricing is available |
| Maki | Native `maki.api_request` logs | Uses positive reported costs, otherwise estimates when possible |

The receiver also accepts OTLP metrics and unused log records from these
clients. It counts them for ingestion diagnostics but does not turn aggregate
signals into duplicate usage rows.

## Installation

### Release binary

Download the binary for your operating system and architecture from
[GitHub Releases](https://github.com/tbshfr/ai-usage/releases), then run the
`ai-usage` executable.

### Build from source

Building requires Go 1.26.7 or newer. The project does not require CGO, Node,
or a frontend toolchain.

```sh
git clone https://github.com/tbshfr/ai-usage.git
cd ai-usage
go build -o ai-usage ./cmd/ai-usage
./ai-usage --otlp-http 127.0.0.1:4318
```

### Container

The image is published at `ghcr.io/tbshfr/ai-usage`. The included
[`compose-example.yaml`](compose-example.yaml) is intended for a deployment
behind an HTTPS reverse proxy. Copy [`.env.example`](.env.example) to `.env`,
set its credentials, and create the writable bind-mount directory. The image
runs as UID and GID `65532`:

```sh
mkdir -p data
sudo chown 65532:65532 data
```

Attach the Compose service to your proxy network, then start it with:

```sh
docker compose -f compose-example.yaml up -d
```

See [configuration and deployment](docs/configuration.md) before exposing the
dashboard or an OTLP receiver outside the local machine.

## Privacy

`ai-usage` stores request metadata and token counts, not prompts or model
responses. Stored fields can include timestamps, source, provider, model,
token counts, duration, conversation and trace identifiers, agent names,
repository metadata, and cost information.

Some clients send content-bearing attributes even when their content-capture
option is disabled. The normalizers read an explicit allowlist of usage fields
and never persist raw telemetry payloads. Application logs contain counts and
operational metadata, not prompt or completion content.

Usage stays on the host unless you enable S3-compatible backups. Cost
estimation fetches the public OpenRouter model catalog after usage arrives; the
request contains no API key or usage telemetry.

## Cost data

Costs reported by a client take priority. For requests without a reported
cost, `ai-usage` tries the cached OpenRouter catalog, then the bundled or
configured manual prices. Model names ending in `free`, including `:free` and
`-free`, receive a zero cost. A request remains unpriced if no rule matches.

The UI and API label reported, estimated, free, and unknown costs separately.
See [cost estimation](docs/configuration.md#cost-estimation) for matching,
refresh, and manual-price details.

## Data location

The default data directory is:

- Linux: `~/.local/share/ai-usage` (or `$XDG_DATA_HOME/ai-usage`)
- macOS: `~/Library/Application Support/ai-usage`
- Windows: `%LOCALAPPDATA%\ai-usage`

The SQLite database is `usage.db` inside that directory. To delete local
history, stop the process and remove the database together with any
`usage.db-wal` and `usage.db-shm` files. Remote backups remain until their
retention policy or an operator removes them.

## Documentation

- [Client setup](docs/client-setup.md)
- [Configuration and deployment](docs/configuration.md)
- [JSON API](docs/api.md)
- [Backups and restore](docs/backups.md)
- [Development](docs/development.md)
- [Telemetry mapping](docs/telemetry.md)

## Development

Contributor notes, repository layout, fixture capture, and release commands are
in [docs/development.md](docs/development.md).
