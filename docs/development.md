# Development

## Requirements

- Go 1.26.7 or newer
- No CGO
- No Node or frontend build step

Build and run the application with:

```sh
go build -o ai-usage ./cmd/ai-usage
./ai-usage --otlp-http 127.0.0.1:4318
```

## Repository layout

| Path | Purpose |
|---|---|
| `cmd/ai-usage` | Application entry point and process-level tests |
| `cmd/capture` | Local raw OTLP capture tool for refreshing fixtures |
| `cmd/inspect` | OTLP fixture inspection tool |
| `cmd/sanitize` | OTLP fixture redaction and JSON conversion |
| `internal/ingest` | OTLP/HTTP and OTLP/gRPC receivers |
| `internal/normalize` | Source detection and canonical generation mapping |
| `internal/storage` | SQLite schema, migrations, and queries |
| `internal/pricing` | OpenRouter and manual cost enrichment |
| `internal/backup` | Snapshot, compression, upload, and scheduling |
| `internal/api` | JSON API and health endpoints |
| `internal/web` | Dashboard handlers and rendering |
| `web` | Embedded templates and static assets |
| `testdata` | Sanitized OTLP fixtures captured from supported clients |

The main data path is:

```text
OTLP receiver -> source normalizer -> SQLite -> dashboard and JSON API
                                      |
                                      +-> cost enrichment
```

## Invariants

- Each source has one authoritative per-call signal. Aggregate spans, metrics,
  and duplicate log events do not create generations.
- Stable source identifiers produce deterministic record IDs. Retried exports
  merge newly reported fields without duplicating requests.
- Missing token and cost fields remain `NULL`; an explicit zero remains zero.
- Copilot and Codex input counts include cached tokens, while OpenCode and Maki
  report uncached input separately. API and UI output convert them to the same
  disjoint token buckets without changing raw stored values.
- Copilot and Codex reasoning counts are included in their reported output;
  OpenCode reports output and reasoning separately. Canonical totals and
  estimated prices use each source's convention.
- Raw OTLP payloads, prompt text, completions, tool data, and identity fields
  are never persisted or logged.
- The project uses a pure-Go SQLite driver so release binaries do not depend on
  a system C library.

The observed fields and signal choices are documented in
[telemetry.md](telemetry.md).

## Verification

Run the checks used by CI:

```sh
go mod tidy
git diff --exit-code go.mod go.sum
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
```

Tests use temporary databases and local HTTP servers. They do not require an
OpenRouter key or a real S3 bucket.

## Embedded web assets

`assets.go` embeds templates, static files, and public files. A file below
`web/public` is served at the matching root URL, while files below `web/static`
are served from `/static` with content hashes.

The `web/public` tree is intentionally public. Hidden paths, directories, and
first path segments reserved by application routes are rejected. Update
`reservedSegments` in `assets.go` if a new top-level application route is
added.

Third-party browser assets are committed under `web/static/vendor`; their
versions and sources are recorded in `web/static/vendor/VENDORED.md`.

## Refreshing telemetry fixtures

Raw captures can contain prompts, responses, tool arguments, filesystem paths,
and account identifiers. Never commit a raw capture.

### Capture a client

Create a temporary directory and start the capture receiver:

```sh
mkdir -p /tmp/ai-usage-capture/opencode
go run ./cmd/capture /tmp/ai-usage-capture/opencode
```

Point one client at `http://127.0.0.1:4318` and generate:

1. A plain request.
2. A request that uses a tool.
3. A multi-turn conversation.
4. Any client-specific helper traffic, such as title generation or
   autocomplete.

Use separate directories for OpenCode, Copilot, Codex, and Maki. Keep prompt
capture disabled where the client exposes that option.

The receiver accepts `POST /v1/traces`, `/v1/metrics`, and `/v1/logs`. It writes
each body unchanged as `<kind>-<unix-milliseconds>-<counter>.<ext>` with mode
`0600`. JSON bodies use `.json`; other bodies use `.pb`.

### Sanitize the capture

Convert selected payloads into the appropriate `testdata` directory:

```sh
go run ./cmd/sanitize traces /tmp/ai-usage-capture/copilot/traces-123.pb \
  > testdata/copilot/traces-chat-simple.json
go run ./cmd/sanitize metrics /tmp/ai-usage-capture/opencode/metrics-123.pb \
  > testdata/opencode/metrics.json
go run ./cmd/sanitize logs /tmp/ai-usage-capture/codex/logs-123.json \
  > testdata/codex/logs-sse-events.json
```

`cmd/sanitize` accepts OTLP/JSON and OTLP/protobuf. It redacts known content,
identity, path, and correlation fields before producing formatted OTLP/JSON.

Audit every new fixture before committing it:

```sh
rg -n 'REDACTED' testdata
rg -ni '@|/home/|/Users/|workdir|tbshfr' testdata
go test ./internal/ingest ./internal/normalize ./internal/sanitizeotel
```

Update the fixture-provenance table in [telemetry.md](telemetry.md) with the
client version and the source batch represented by each fixture.

## Releases

Build all supported archives and checksums with:

```sh
scripts/release.sh v1.2.3
```

Artifacts are written to `dist`. The version is embedded with `-ldflags`; when
the argument is omitted, the script uses `git describe` and falls back to
`dev`.

The container workflow builds tagged images for `linux/amd64` and
`linux/arm64`, publishing both the tag and `latest` to GHCR.
