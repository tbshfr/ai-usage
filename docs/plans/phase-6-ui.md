# Phase 6 — Dashboard UI

Read `docs/plans/README.md` first. Prerequisite: Phase 5 merged (JSON API).

## Goal

The user-facing dashboard: server-rendered pages (html/template) with HTMX
for interactions, all assets embedded in the binary. Fast, local, no build
step, no framework.

## Steps

### 0. Asset acquisition (no npm)

- Download once, commit under `web/static/vendor/`:
  - `htmx.min.js` (from https://htmx.org, pinned version, note it in
    `web/static/vendor/VENDORED.md` with URLs + versions)
  - A charting library as a single file — uPlot (`uplot.min.js` +
    `uplot.css`) preferred (small); Chart.js IIFE build acceptable.
- These are the only vendored files. No CDN references at runtime.

### 1. Layout — `internal/web/`

- `//go:embed` templates + static (via `web/templates/*.html`,
  `web/static/**`; keep the embed directives in `internal/web/embed.go`
  pointing at the repo-root `web/` dir — use `embed` from a root-level
  `web.go` if import cycles make internal embedding awkward; pick the
  simplest working arrangement).
- One base template: minimal nav (Overview / Models / Recent / Detail is
  reached by click), shared filter bar, dark-friendly clean CSS
  (`web/static/app.css`, hand-written, small).
- No JavaScript beyond htmx + the chart lib + ≤50 lines of glue.

### 2. Pages & fragments

Server-side rendering hitting the Phase 4 query layer directly (not via
HTTP-to-self). HTMX fragments (`hx-get`) re-render partials when filters
change — every fragment is also a full-HTML-renderable page section.

**Overview** (`/`):
- Cards: Today / This week / This month / All time — each: requests,
  input, output, cache read, cache creation, reasoning tokens; cost line
  only where `costKnownCount > 0`: `≈ $X (N known, M without cost data)`;
  all-unknown → render `—` (never `$0.00`).
- Filter bar: date range presets (Today, 7d, 30d, This month, All) +
  source/provider/model dropdowns (from Distinct* queries) — same params as
  the API, shared parsing helper.
- Usage-over-time chart (uPlot): stacked or grouped token series per day,
  granularity toggle day/week/month; cost series plotted only from
  cost-known buckets (label the series accordingly).

**Breakdowns** (one page, two/three tables or tabs):
- By source: requests, tokens, cost (nullable) — sources render with a
  friendly name (`opencode` → OpenCode, `copilot` → VS Code Copilot).
- By provider: raw provider + the display helper's underlying provider for
  copilot model families (e.g. `github` rows get a breakdown by model
  beneath or beside — implement as: provider table shows raw provider;
  directly under it, a model table (ByModel) serves as the real drill-down).
- By model: Model / Requests / Input / Output / Cache read / Cache creation
  / Reasoning / Cost (nullable `—`).

**Recent** (`/generations`):
- Table: Time, Source, Provider, Model, Input, Output, Cache read, Cache
  creation, Reasoning, Cost, Duration. Newest first, 50/page (limit/offset
  pager with prev/next HTMX buttons). Row links to detail.

**Detail** (`/generations/{id}`):
- All fields of the record incl. trace/span/conversation IDs (copyable),
  duration, git repo/branch when present, cost (or the explicit
  "not reported by this source" note). No raw payload exists to show — by
  design.

**Fragment endpoints** mirror pages: `/fragments/overview-cards`,
`/fragments/timeseries`, `/fragments/recent-rows`, etc. — used by HTMX.

### 3. Formatting rules

- Timestamps rendered in the **browser's local time** via a tiny JS
  formatter *or* server-side UTC with a visible "UTC" label — pick one, be
  consistent, keep server-side simple (UTC + label is fine for v1).
- Tokens: thousands separators (e.g. `19,452`). Durations: `3.2s` / `1m 12s`.
- Cost: `$1.2345` (4 decimals when < $10; 2 otherwise) — display-only.
- Missing values: `—` (em dash) everywhere, never `0`.

### 4. Tests — `internal/web/` (+ where useful)

- Template render tests with the Phase 4 seed: assert key content in HTML
  (e.g. the em-dash for copilot cost, token numbers, friendly source names).
- Fragment endpoints: filter change narrows rendered rows (substring
  assertions on rendered HTML).
- Detail page: 404 on unknown id.
- `embed` sanity test: assets served with correct content types; 200s.

## Acceptance criteria

- [ ] All pages render from the seeded DB with correct, tested HTML.
- [ ] Filter bar changes actually re-query server-side (HTMX fragments) —
      verified in tests, and manually once with real dogfood data.
- [ ] Cost-null rendering contract: copilot-only view shows `—`, tested.
- [ ] Binary size stays reasonable (< ~25 MB) and everything is embedded:
      `./ai-usage` serves the full UI from an empty working directory
      (test: run from `/` or a temp dir).
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- No React/Vue/Svelte, no bundler, no npm at any point.
- No loading the database client-side — all aggregation stays in SQL.
- No charts for data we don't have (no cost chart when zero cost-known rows).
- Do not render prompt/completion content anywhere — it never reaches the
  DB anyway; there is nothing to render.

## Report back

Pages/fragments implemented, vendored file list + versions, UTC-vs-local
decision, template test coverage summary.
