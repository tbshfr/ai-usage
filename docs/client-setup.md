# Client setup

Start `ai-usage` with the OTLP/HTTP receiver before configuring a client:

```sh
ai-usage --otlp-http 127.0.0.1:4318
```

The dashboard's **Setup** page (`/setup`) shows the same configuration for
each client, with the receiver URL and bearer-token lines filled in.

The examples use a local receiver. For a remote receiver, use an HTTPS endpoint
and configure the bearer token in the same client section. The server must set
`AI_USAGE_OTLP_TOKEN` to the same value.

## OpenCode

OpenCode sends telemetry through the third-party
[`@devtheops/opencode-plugin-otel`](https://github.com/devtheops/opencode-plugin-otel)
plugin. Add it to `~/.config/opencode/opencode.json`:

```jsonc
{
  "plugin": [
    [
      "@devtheops/opencode-plugin-otel",
      {
        "enabled": true,
        "endpoint": "http://127.0.0.1:4318",
        "protocol": "http/protobuf",
        "metricPrefix": "opencode.",
        "resourceAttributes": "deployment.environment=production",
        "disabledTraces": ["tool"]
      }
    ]
  ]
}
```

For an authenticated remote receiver, change `endpoint` to its HTTPS origin
and add:

```jsonc
"otlpHeaders": "{env:AI_USAGE_OTLP_HEADERS}"
```

Export the secret before starting OpenCode:

```sh
export AI_USAGE_OTLP_HEADERS='Authorization=Bearer <token>'
```

The plugin can also be configured without editing the JSON file:

```sh
export OPENCODE_ENABLE_TELEMETRY=true
export OPENCODE_OTLP_ENDPOINT=http://127.0.0.1:4318
export OPENCODE_OTLP_PROTOCOL=http/protobuf
```

For gRPC, start `ai-usage` with `--otlp-grpc 127.0.0.1:4317`, set the endpoint
to `http://127.0.0.1:4317`, and set the protocol to `grpc`.

The plugin may emit content-bearing span attributes. `ai-usage` only reads its
usage-field allowlist and never stores those attributes. Leave optional prompt
capture disabled. Reported costs take priority; missing costs can use the local
pricing fallback.

Verify after an OpenCode request:

```sh
curl -s 'http://127.0.0.1:8080/api/generations?source=opencode&limit=1' | jq '.[0]'
```

## Codex CLI

Codex has a native OTLP log exporter. Add this block to the user-level
`~/.codex/config.toml`. Project-level `.codex/config.toml` files cannot set
`otel`.

```toml
[otel]
environment = "production"
log_user_prompt = false
exporter = { otlp-http = {
  endpoint = "http://127.0.0.1:4318/v1/logs",
  protocol = "json"
} }
```

For an authenticated remote receiver, use its HTTPS URL ending in `/v1/logs`
and add a header inside the `otlp-http` object:

```toml
headers = { "authorization" = "Bearer ${AI_USAGE_OTLP_TOKEN}" }
```

Export `AI_USAGE_OTLP_TOKEN` before starting Codex. Keep
`log_user_prompt = false`. Other Codex events can still contain tool data or
identity metadata, but `ai-usage` reads only the terminal token event fields.

Verify after completing a prompt and allowing Codex to flush its asynchronous
export batch, which may happen at shutdown:

```sh
curl -s 'http://127.0.0.1:8080/api/generations?source=codex&limit=1' \
  | jq '.[0] | {model,inputTokens,outputTokens,reasoningTokens}'
```

## Maki

Maki has built-in OTLP telemetry. Add the telemetry table to `init.lua`:

```lua
maki.setup({
    telemetry = {
        enabled = true,
        metrics_exporter = "none",
        logs_exporter = "otlp",
        protocol = "http/protobuf",
        endpoint = "http://127.0.0.1:4318",
    },
})
```

For an authenticated remote receiver, change `endpoint` to its HTTPS origin
and add this field to the telemetry table:

```lua
headers = { ["authorization"] = "Bearer <token>" },
```

Keep the token out of a committed `init.lua`. Maki also accepts
`OTEL_METRICS_EXPORTER=none`; environment variables take priority over
`init.lua`.

Leave `log_user_prompts` and `log_tool_details` disabled. `ai-usage` stores one
generation for each `maki.api_request` event and ignores interval metrics and
other events. A positive `cost_usd` is treated as reported; a zero estimate is
left for the local pricing fallback.

Maki's [telemetry documentation](https://maki.sh/docs/telemetry/) also describes
gRPC. Set `protocol = "grpc"`, use `http://127.0.0.1:4317`, and start
`ai-usage` with `--otlp-grpc 127.0.0.1:4317`.

Maki currently exports no reasoning-token count or subagent identifier on
`maki.api_request` events. Reasoning appears as unknown in `ai-usage`, and
subagent calls normally join the parent session's totals and cache hit rate.
Separate subagent sessions start with empty context, so the session cache hit rate can appear lower than in other harnesses.

Verify after a Maki API call and one export interval:

```sh
curl -s 'http://127.0.0.1:8080/api/generations?source=maki&limit=1' \
  | jq '.[0] | {model,inputTokens,cacheReadTokens,outputTokens,cost}'
```

## Claude Code

Claude Code has built-in OpenTelemetry support, configured with environment
variables. Add them to the `env` block of `~/.claude/settings.json` so every
session exports:

```jsonc
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_METRICS_EXPORTER": "none",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318"
  }
}
```

For an authenticated remote receiver, change the endpoint to its HTTPS origin
and add:

```jsonc
"OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer <token>"
```

Leave `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`,
`OTEL_LOG_TOOL_DETAILS`, and `OTEL_LOG_RAW_API_BODIES` unset. `ai-usage`
stores one generation for each `claude_code.api_request` event, including
helper calls such as session-title generation, and ignores metrics and other
events. Claude Code's reported `cost_usd` is used when positive; a zero
estimate is left for the local pricing fallback. The reasoning-effort setting
(`effort`) is shown on each request. The `api_request` event has no
reasoning-token count, so reasoning appears as unknown.

Claude Code batches logs and exports every 5 seconds by default
(`OTEL_LOGS_EXPORT_INTERVAL`, in milliseconds). Verify after a prompt:

```sh
curl -s 'http://127.0.0.1:8080/api/generations?source=claude-code&limit=1' \
  | jq '.[0] | {model,inputTokens,cacheReadTokens,outputTokens,cost}'
```

## VS Code GitHub Copilot

Copilot Chat has native OpenTelemetry support. Open the VS Code user
`settings.json` and add:

```jsonc
{
  "github.copilot.chat.otel.enabled": true
}
```

Its default endpoint is `http://localhost:4318`, so local use needs no other
setting. Keep `captureContent` at its `false` default.

For an authenticated remote receiver, start VS Code with the standard OTLP
variables:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://ai-usage.example.com \
OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer <token>' \
code
```

On Windows, store the header in the user environment before starting VS Code:

```powershell
[Environment]::SetEnvironmentVariable(
  "OTEL_EXPORTER_OTLP_HEADERS",
  "Authorization=Bearer <token>",
  "User"
)
```

`COPILOT_OTEL_PROTOCOL` selects `otlp-http` (the default) or `otlp-grpc`.
The `file` and `console` exporters do not send data to `ai-usage`. Copilot's
[agent monitoring guide](https://code.visualstudio.com/docs/agents/guides/monitoring-agents)
documents the integration.

Copilot does not report a cost. `ai-usage` estimates one when the model and
token fields match available pricing.

Verify after a Copilot Chat request:

```sh
curl -s 'http://127.0.0.1:8080/api/generations?source=copilot&limit=1' | jq '.[0]'
```

## Troubleshooting

Check the ingestion counters when a client does not appear:

```sh
curl -s http://127.0.0.1:8080/api/stats | jq
```

- `received` should increase when a client exports.
- `stored` increases for new generation records.
- `deduplicated` increases when a client retries a batch already stored.
- `ignoredNotUsed` includes aggregate metrics and non-terminal logs.
- `normalizationErrors` indicates malformed generation telemetry.

An authenticated receiver returns `401` when the bearer token is missing or
incorrect. These failures appear as `request rejected` in the server log and
do not enter the normalization pipeline.
