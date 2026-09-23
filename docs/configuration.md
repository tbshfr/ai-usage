# Configuration and deployment

Command-line flags override environment variables. Environment variables
override defaults.

## Options

| Flag | Environment variable | Default | Purpose |
|---|---|---|---|
| `--http` | `AI_USAGE_HTTP_ADDR` | `127.0.0.1:8080` | Dashboard and JSON API address; an empty value disables it |
| `--otlp-http` | `AI_USAGE_OTLP_HTTP_ADDR` | disabled | OTLP/HTTP address |
| `--otlp-grpc` | `AI_USAGE_OTLP_GRPC_ADDR` | disabled | OTLP/gRPC address |
| `--data-dir` | `AI_USAGE_DATA_DIR` | OS user-data directory plus `ai-usage` | Data directory |
| `--database` | `AI_USAGE_DATABASE` | `<data-dir>/usage.db` | SQLite path |
| `--pricing-file` | `AI_USAGE_PRICING_FILE` | bundled prices only | Additional manual model-price file |
| `--log-level` | `AI_USAGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `--dashboard-user` | `AI_USAGE_DASHBOARD_USER` | unset | Dashboard username |
| `--dashboard-password` | `AI_USAGE_DASHBOARD_PASSWORD` | unset | Dashboard password |
| `--otlp-token` | `AI_USAGE_OTLP_TOKEN` | unset | Bearer token required from OTLP clients |
| `--backup-s3-bucket` | `AI_USAGE_BACKUP_S3_BUCKET` | disabled | Backup bucket |
| `--backup-s3-region` | `AI_USAGE_BACKUP_S3_REGION` | required with backups | S3 region; use `auto` for R2 |
| `--backup-s3-prefix` | `AI_USAGE_BACKUP_S3_PREFIX` | required with backups | Dedicated object prefix ending in `/` |
| `--backup-s3-endpoint` | `AI_USAGE_BACKUP_S3_ENDPOINT` | AWS S3 | S3-compatible endpoint |
| `--backup-s3-access-key-id` | `AI_USAGE_BACKUP_S3_ACCESS_KEY_ID` | unset | Backup access key ID |
| `--backup-s3-secret-access-key` | `AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY` | unset | Backup secret access key |
| `--backup-s3-session-token` | `AI_USAGE_BACKUP_S3_SESSION_TOKEN` | unset | Optional backup session token |

Passing an empty listener value disables that listener. For example,
`--http ""` runs without the dashboard and API.

## Local listeners

This command starts the dashboard and OTLP/HTTP on loopback:

```sh
ai-usage --otlp-http 127.0.0.1:4318
```

To receive OTLP/gRPC instead, or alongside HTTP, add:

```sh
ai-usage --otlp-http 127.0.0.1:4318 --otlp-grpc 127.0.0.1:4317
```

The dashboard is available at `http://127.0.0.1:8080`. Each client's exact
endpoint is documented in [client setup](client-setup.md).

## Authentication

Unauthenticated listeners may bind only to loopback addresses. Startup fails
if a listener uses `:8080`, `0.0.0.0`, a public IP, or a hostname without the
credentials for that listener.

Dashboard authentication requires both values:

```sh
export AI_USAGE_DASHBOARD_USER=admin
export AI_USAGE_DASHBOARD_PASSWORD='a-long-random-password'
```

The dashboard uses an HMAC-signed, `HttpOnly` session cookie that lasts seven
days. Its signing secret is generated at startup, so restarting the process
logs the user out.

OTLP authentication uses one bearer token for both HTTP and gRPC:

```sh
export AI_USAGE_OTLP_TOKEN='a-long-random-token'
```

Generate credentials with a password manager or a command such as
`openssl rand -hex 32`. Keep them outside the repository and command history.
Client-specific header configuration is included in each section of
[client setup](client-setup.md).

`GET /health` and `GET /ready` remain public for probes. All other dashboard
and API routes require a login when dashboard authentication is enabled.

## Remote deployment

Run the service behind an HTTPS reverse proxy. The binary serves plain HTTP
and trusts the proxy to terminate TLS.

A minimal process configuration is:

```sh
export AI_USAGE_HTTP_ADDR=:8080
export AI_USAGE_OTLP_HTTP_ADDR=:4318
export AI_USAGE_DASHBOARD_USER=admin
export AI_USAGE_DASHBOARD_PASSWORD='a-long-random-password'
export AI_USAGE_OTLP_TOKEN='a-long-random-token'
exec ./ai-usage
```

The login limiter allows three failed attempts per client IP in 15 minutes and
100 failed attempts globally in the same window. It attributes a request to
the rightmost `X-Forwarded-For` entry, which is safe only behind a proxy that
appends the observed peer address. Caddy and nginx do this by default. Do not
expose the dashboard port directly to untrusted clients, because a direct
client controls that header.

The included Compose example expects:

- a pre-existing external network named `proxy`;
- a reverse proxy on that network;
- a writable `./data` directory;
- dashboard and OTLP credentials in `.env`.

The container runs with a read-only root filesystem. It writes the database,
backup state, and temporary backup files below `/data`.
The image runs as UID and GID `65532`, so prepare a bind mount with
`mkdir -p data && sudo chown 65532:65532 data` unless your container runtime
maps ownership for you.

## PWA

Open the dashboard over HTTPS in a Browser on Android or iOS and select **Add to home
screen**, then **Install**. The installed app is named **AI Usage**.

Most Browsers permit installation from localhost for
development; other hosts require HTTPS.

## Cost estimation

The first batch of usage starts a background OpenRouter catalog fetch. The
catalog is cached in SQLite for 24 hours and reused after restarts. A refresh
failure does not reject telemetry. The last successful catalog remains usable;
without one, paid requests wait for a retry after five minutes.

Cost selection follows this order:

1. A cost reported by the client, including an explicit zero.
2. Zero for a trimmed, case-insensitive model name ending in `free`.
3. A matching OpenRouter price.
4. A matching manual price.
5. Unknown cost.

Paid estimates require input and output counts. Missing optional token buckets
count as zero. Calculations can include input, output, reasoning, cache read,
cache write, and fixed per-request rates. Missing cache rates use the normal
input rate. Conditional rates use the full prompt count and request timestamp
in UTC.

Matching uses exact OpenRouter IDs, unique bare IDs, and explicit aliases for
known client model names. Estimates cover reported token use and request
charges. They cannot include image, search, audio, or other billable units that
the client does not report, and they may differ from provider invoices or
subscription charges.

The rate snapshot used for a request is retained. A later catalog refresh does
not reprice saved estimates. Later telemetry can fill missing token fields with
the saved rates, and a later client-reported cost replaces an estimate.

Existing unpriced requests receive one historical backfill after a fresh
catalog becomes available for that database. The backfill uses prices available
at that time. It does not reconstruct historical catalogs, rerun after later
catalog changes, or revisit models that remained unknown.

### Manual prices

Bundled fallback prices are in
[`internal/pricing/manual-prices.json`](../internal/pricing/manual-prices.json).
Changing that file requires rebuilding the binary. To supplement or replace an
entry at runtime, pass `--pricing-file /path/to/prices.json` or set
`AI_USAGE_PRICING_FILE`, then restart the process.

```json
{
  "models": {
    "mai-code-1.1-flash": {
      "inputPerMillion": 0.20,
      "outputPerMillion": 1.20,
      "cacheReadPerMillion": 0.02,
      "updatedAt": "2026-09-21",
      "sourceURL": "https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing"
    }
  }
}
```

Rates are USD per million tokens. Each entry requires `inputPerMillion`,
`outputPerMillion`, and an `updatedAt` date in `YYYY-MM-DD` form. Optional
`cacheReadPerMillion` and `cacheWritePerMillion` default to the input rate;
`reasoningPerMillion` defaults to the output rate. Explicit zero values are
valid. `sourceURL` is an optional maintenance reference.

Configured entries replace bundled entries with the same exact, trimmed model
ID. They apply only when OpenRouter does not match the model. Invalid files stop
startup instead of being ignored.

## Backups

Backups are disabled unless `AI_USAGE_BACKUP_S3_BUCKET` or its flag is set.
See [backups and restore](backups.md) for Cloudflare R2 setup, retention,
monitoring, and recovery.
