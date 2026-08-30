# Telemetry capture instructions (Phase 1)

This walks you through generating real telemetry from OpenCode and VS Code
Copilot into the Phase 1 capture harness. The captured data becomes the
sanitized fixtures that later phases build against.

## 1. Start the capture harness

```bash
go build -o ai-usage ./cmd/ai-usage
./ai-usage --dump-dir /tmp/ai-usage-capture/opencode
```

It listens on `:4318` (OTLP/HTTP). Every received batch is written unchanged to
the dump directory plus an `index.jsonl` summary.

## 2. OpenCode

Add the OTel plugin to `~/.config/opencode/opencode.json`:

```jsonc
{
  "plugin": [["@devtheops/opencode-plugin-otel", {
    "enabled": true,
    "endpoint": "http://localhost:4318",
    "protocol": "http/protobuf"
  }]]
}
```

Env-var alternative: `OPENCODE_ENABLE_TELEMETRY=1`,
`OPENCODE_OTLP_ENDPOINT=http://localhost:4318`,
`OPENCODE_OTLP_PROTOCOL=http/protobuf`.

Prompts are NOT captured unless `OPENCODE_CAPTURE_PROMPT_IN_LOGS` is set —
leave it unset.

Run the harness with `--dump-dir /tmp/ai-usage-capture/opencode` and use
opencode normally:

1. One plain question.
2. One request that triggers a tool call.
3. One multi-turn conversation (so cache behavior shows up).
4. Usage of the small model if configured (e.g. title generation).

## 3. VS Code GitHub Copilot

Add to VS Code `settings.json`:

```jsonc
{
  "github.copilot.chat.otel.enabled": true
  // endpoint already defaults to http://localhost:4318 (OTLP HTTP)
  // captureContent stays false — do not enable it
}
```

Restart VS Code, run the harness with
`--dump-dir /tmp/ai-usage-capture/copilot`, and use it:

1. One plain chat question.
2. One agent-mode task that calls tools and runs at least 2 LLM round-trips.
3. One longer multi-turn conversation.
4. Inline chat / tab completion if available.

## 4. Done?

Check the dump directories contain non-empty files:

```bash
ls -la /tmp/ai-usage-capture/opencode /tmp/ai-usage-capture/copilot
```

Then tell the agent the capture is complete. Nothing else is needed — the
agent handles inspection, sanitization, fixtures, and `docs/telemetry.md`.
