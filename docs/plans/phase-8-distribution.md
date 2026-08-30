# Phase 8 — Documentation and distribution

Read `docs/plans/README.md` first. Prerequisite: Phase 7 merged.

## Goal

Make the project usable by a fresh machine: README with quickstart, source
setup docs with exact copy-paste configs, cross-platform release builds, and
an optional Docker convenience image — in that order of importance.

## Steps

### 1. `README.md`

- What it is (3–5 sentences): local AI usage dashboard for OpenCode and VS
  Code Copilot; one binary + one SQLite file; receives OTLP directly; cost
  shown only where the source reports it.
- Quickstart: download binary for your OS → run `./ai-usage` → banner →
  configure the two tools (link to `docs/source-setup.md`).
- Build from source: `go build ./cmd/ai-usage` (Go ≥1.26, no CGO).
- Configuration table (flags/env/defaults from Phase 2).
- Data location & privacy section: what is and isn't collected (metadata
  and token counts only; no prompts/completions; nothing leaves the
  machine; localhost-bound).
- Screenshots: take 2–3 (overview, breakdowns, detail) from the running
  app with real dogfood data — redact repo URLs if sensitive. Store in
  `docs/screenshots/`.
- Development: layout pointer to `docs/plans/README.md` and the phase
  plans, `go test ./...`.

### 2. `docs/source-setup.md` — the copy-paste configs

**OpenCode** (`~/.config/opencode/opencode.json`):

```jsonc
{
  "plugin": [["@devtheops/opencode-plugin-otel", {
    "enabled": true,
    "endpoint": "http://localhost:4318",
    "protocol": "http/protobuf"
  }]]
}
```

- Env-var alternative documented (`OPENCODE_ENABLE_TELEMETRY`,
  `OPENCODE_OTLP_ENDPOINT`, `OPENCODE_OTLP_PROTOCOL`; gRPC `:4317` also
  works).
- Note the plugin is third-party; link its repo; note prompts are not
  captured unless the user explicitly enables prompt capture.
- Mention: cost numbers come from opencode's own pricing estimates.

**VS Code Copilot** (`settings.json`):

```jsonc
{ "github.copilot.chat.otel.enabled": true }
```

- Endpoint default is already `http://localhost:4318` — zero extra config.
- Document optional env-var route (`OTEL_EXPORTER_OTLP_ENDPOINT`,
  `COPILOT_OTEL_PROTOCOL`), the `otlp-grpc`/`file`/`console` exporter
  types, and that `captureContent` must stay false.
- Note: requires a current VS Code version (link
  https://code.visualstudio.com/docs/agents/guides/monitoring-agents);
  telemetry level must not be `off`.
- Note: Copilot reports no cost — the dashboard shows tokens for it.

**Verification section** for each source: the expected observable result
(e.g. `curl localhost:8080/api/summary` shows nonzero `requests` after a
few minutes of usage; `GET /api/stats` counters move).

### 3. Release builds

- `scripts/release.sh` (or Makefile target `release`):
  ```bash
  CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o dist/ai-usage-linux-amd64  ./cmd/ai-usage
  # likewise: linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 (name it ai-usage-windows-amd64.exe)
  ```
- Version injection: a `main.version` var (default `dev`) shown in the
  banner and `GET /health` response.
- Checksums: `sha256sum dist/* > dist/checksums.txt`.
- Optional: a `goreleaser.yml` only if it adds value over the script; the
  script is the source of truth.
- Cross-compile smoke test in CI or locally: build all five, run the linux
  one, `/health` 200 (others: at least `go build` success; runtime smoke on
  your platform).

### 4. Optional Docker image (last, only if everything above works)

- Multi-stage: `golang:1.26` builder → `scratch`/`distroless-static`
  runtime; single `COPY` of the binary; `VOLUME /data`;
  `ENV AI_USAGE_DATA_DIR=/data`; ports 4317/4318/8080; a `docker run`
  example. Do not make Docker the primary distribution.

### 5. Repo hygiene

- `docs/CAPTURE-INSTRUCTIONS.md` from Phase 1: keep (useful for future
  fixture refreshes) but move under `docs/` if it drifted; ensure fixture
  refresh process is described in one place.
- `docs/telemetry.md`, `docs/api.md`, `docs/source-setup.md`,
  `docs/plans/*` — all current (phases marked done).
- `.gitignore`: `dist/`, `*.db`, dump dirs.

## Acceptance criteria

- [ ] Fresh-clone simulation: on a clean checkout, `go build ./cmd/ai-usage`
      and `go test ./...` pass; the binary runs zero-config with the
      documented banner.
- [ ] All five release targets build with `CGO_ENABLED=0`; checksums file
      generated; linux-amd64 smoke-tested via `/health`.
- [ ] `docs/source-setup.md` configs are copy-pasteable and verified against
      a live dogfood run (both sources show up in the dashboard).
- [ ] README contains quickstart, config table, privacy statement,
      screenshots.
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- No auto-update mechanism, no telemetry-from-the-dashboard, no accounts.
- Don't let the Docker image become a prerequisite for anything.

## Report back

Release artifact list + checksums, docs index, any manual verification
results (dogfood run with both sources), and the final definition-of-done
checklist from `docs/plans/README.md` answered with real numbers.
