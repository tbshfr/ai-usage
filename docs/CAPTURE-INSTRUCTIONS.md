# Telemetry capture instructions (fixture refresh)

This is the single source of truth for refreshing the sanitized fixtures
under `testdata/opencode/` and `testdata/copilot/`. Run it when a source
tool or plugin changes its telemetry.

The current app does not persist raw payloads, so capture into a small
throwaway receiver first, then sanitize. **Never commit unsanitized
captures.**

## 1. Capture raw OTLP

Create a throwaway capture receiver in a scratch directory (outside this
repo) and run it:

```go
// main.go — minimal OTLP/HTTP capture receiver; module with
// go.opentelemetry.io/collector/pdata and the standard library only.
package main

import (
	"fmt"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"io"
	"net/http"
	"os"
)

func main() {
	for _, route := range []struct {
		path string
		kind string
	}{{"/v1/traces", "traces"}, {"/v1/metrics", "metrics"}, {"/v1/logs", "logs"}} {
		kind := route.kind
		http.HandleFunc(route.path, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			name := fmt.Sprintf("%s/%s-%d.%s", os.Args[1], kind, len(body), encoding(r))
			os.WriteFile(name, body, 0o600)
			w.WriteHeader(200)
		})
	}
	http.ListenAndServe("127.0.0.1:4318", nil)
}

func encoding(r *http.Request) string {
	if r.Header.Get("Content-Type") == "application/json" {
		return "json"
	}
	return "pb"
}
```

```bash
mkdir -p /tmp/ai-usage-capture/opencode /tmp/ai-usage-capture/copilot
go mod init capture && go mod tidy && go run . /tmp/ai-usage-capture
```

## 2. OpenCode

Configure the plugin to point at the capture receiver (see
[`source-setup.md`](source-setup.md) for the full config) and use OpenCode
normally:

1. One plain question.
2. One request that triggers a tool call.
3. One multi-turn conversation (so cache behavior shows up).
4. Usage of the small model if configured (e.g. title generation).

Prompts are NOT captured unless `OPENCODE_CAPTURE_PROMPT_IN_LOGS` is set —
leave it unset.

## 3. VS Code GitHub Copilot

Set `"github.copilot.chat.otel.enabled": true` in VS Code `settings.json`
(endpoint already defaults to `http://localhost:4318`; `captureContent`
stays false), restart VS Code, and use it:

1. One plain chat question.
2. One agent-mode task that calls tools and runs at least 2 LLM round-trips.
3. One longer multi-turn conversation.
4. Inline chat / tab completion if available.

## 4. Sanitize into fixtures

Check the capture directories are non-empty, then sanitize each payload
and write it to `testdata/`:

```bash
go run ./cmd/sanitize traces /tmp/ai-usage-capture/copilot/traces-123.pb > testdata/copilot/traces-chat-simple.json
go run ./cmd/sanitize metrics /tmp/ai-usage-capture/opencode/metrics-123.pb > testdata/opencode/metrics.json
```

`cmd/sanitize` accepts both OTLP/JSON and OTLP/protobuf input and redacts
content-bearing attributes (`input.value`, `gen_ai.output.messages`,
`copilot_chat.user_request`, …) and span status messages to
`"[REDACTED]"`.

Verify no content leaked:

```bash
grep -rn "REDACTED" testdata/ | wc -l   # redactions present
grep -rniE "prompt|completion" --include=*.json testdata/ | grep -v REDACTED
```

Finally, update the "Fixture provenance" table in
[`telemetry.md`](telemetry.md) so every committed fixture maps to its
source batch, and re-run the verification commands from
[`docs/plans/README.md`](plans/README.md).
