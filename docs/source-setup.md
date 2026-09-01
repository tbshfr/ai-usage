# Sending telemetry from OpenCode and VS Code Copilot

This dashboard consumes standard OTLP. Both sources below are configured
once and then report usage automatically. After configuring, verify with
the checks at the end of each section.

Both sources use OTLP/HTTP on `http://localhost:4318` by default. The
dashboard only listens on the OTLP ports you explicitly enable, so start
it with:

```bash
ai-usage --otlp-http :4318
```

(or set `AI_USAGE_OTLP_HTTP_ADDR=:4318`). With that flag, most setups need
no endpoint configuration in the sources at all.

## Authentication

On localhost you don't need any of this. When the dashboard runs on a
VPS (or any non-loopback bind), the server **requires** credentials: it
refuses to start a non-loopback OTLP listener without
`AI_USAGE_OTLP_TOKEN`, and the dashboard without
`AI_USAGE_DASHBOARD_USER`/`AI_USAGE_DASHBOARD_PASSWORD`. Pick one long
random token, e.g. `openssl rand -hex 32`.

Both sources below send `Authorization: Bearer <token>` — the only
header mechanism each of them supports.

### OpenCode

Either set the header env var before starting OpenCode:

```bash
export OPENCODE_OTLP_HEADERS="Authorization=Bearer <token>"
```

or pass it inline in `opencode.json` using env substitution (the token
itself stays in the environment, out of version control):

```jsonc
{
  "plugin": [
    [
      "@devtheops/opencode-plugin-otel",
      {
        "enabled": true,
        "endpoint": "https://ai-usage.example.com:4318",
        "protocol": "http/protobuf",
        "otlpHeaders": "{env:OTEL_HEADERS}",
      },
    ],
  ],
}
```

with `OTEL_HEADERS="Authorization=Bearer <token>"` exported alongside
the endpoint vars.

### VS Code GitHub Copilot

Copilot's OTel integration reads auth headers **only** from the
standard env var (there is no settings.json key for headers). Start VS
Code with:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=https://ai-usage.example.com:4318 \
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>" \
code
```

Set it on windows with

```
[Environment]::SetEnvironmentVariable(
  "OTEL_EXPORTER_OTLP_HEADERS",
  "Authorization=Bearer <token>",
  "User"
)
```

### Verify

Without the token, both clients get `401` responses (visible as
`request rejected` lines in the server log) and nothing is stored. With
it, the verification checks at the end of each section below work
unchanged.

## OpenCode

Add the community OTel plugin to `~/.config/opencode/opencode.json`:

```jsonc
  "plugin": [
    ["@devtheops/opencode-plugin-otel", {
      "enabled": true,
      "endpoint": "https://ai.example.com/",
      "protocol": "http/protobuf",
      "metricPrefix": "opencode.",
      "otlpHeaders": "Authorization=Bearer <token>>",
      "resourceAttributes": "deployment.environment=production",
      "disabledTraces": ["tool"]
    }]
  ],
```

Notes:

- The plugin is **third-party** (not part of OpenCode or this project):
  https://github.com/devtheops/opencode-plugin-otel
- Prompts are **not** captured unless you explicitly enable prompt capture
  in the plugin. Leave it off — this dashboard never needs prompt content.
- Cost numbers come from OpenCode's own pricing estimates (models.dev);
  the dashboard displays them as-is and never computes cost itself.

### Env-var alternative

Instead of the config file, you can set environment variables before
starting OpenCode:

```bash
export OPENCODE_ENABLE_TELEMETRY=true
export OPENCODE_OTLP_ENDPOINT=http://localhost:4318
export OPENCODE_OTLP_PROTOCOL=http/protobuf
```

gRPC also works: start the dashboard with `--otlp-grpc :4317` instead, and
set `OPENCODE_OTLP_ENDPOINT=http://localhost:4317` and
`OPENCODE_OTLP_PROTOCOL=grpc`.

### Verification

After a few minutes of OpenCode usage:

```bash
curl -s localhost:8080/api/summary | jq '.requests'
```

returns a nonzero number, and `"source":"opencode"` records appear at
`http://localhost:8080` (overview and breakdowns). The pipeline counters
move too:

```bash
curl -s localhost:8080/api/stats
```

(`stored` grows on each export; re-sent batches only bump `deduplicated`.)

## VS Code GitHub Copilot

Copilot Chat has built-in OTel support. Add to VS Code `settings.json`
(`Ctrl+Shift+P` → "Preferences: Open User Settings (JSON)"):

```jsonc
{ "github.copilot.chat.otel.enabled": true }
```

The default endpoint is already `http://localhost:4318` — with the
dashboard started as `ai-usage --otlp-http :4318`, no extra config is
needed on either side.

Notes:

- Requires a current VS Code version with agent monitoring support:
  https://code.visualstudio.com/docs/agents/guides/monitoring-agents
- VS Code's telemetry level must not be `off` (`telemetry.telemetryLevel`
  set to `all` or `error` or higher than off).
- `captureContent` must stay `false` (the default). This dashboard never
  needs prompt/completion content.
- Copilot reports **no cost** — the dashboard shows token counts for it,
  with cost shown as unknown.

### Optional env-var route

Instead of settings.json, VS Code can be started with:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 code
```

`COPILOT_OTEL_PROTOCOL` selects the exporter protocol (`otlp-http` is the
default; `otlp-grpc` targets `:4317`, which the dashboard must enable with
`--otlp-grpc :4317`). The exporter types `otlp-grpc`,
`file`, and `console` are supported by Copilot's OTel integration; use
`otlp-http`/`otlp-grpc` with this dashboard — `file` and `console` write
somewhere other than the receiver.

### Verification

After a few minutes of Copilot Chat usage:

```bash
curl -s localhost:8080/api/summary | jq '.requests'
```

returns a nonzero number and `"source":"copilot"` records appear at
`http://localhost:8080`. `GET /api/stats` counters move as new exports
arrive:

```bash
curl -s localhost:8080/api/stats
```
