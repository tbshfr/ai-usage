# Phase 4 — Persistence & aggregation queries

Read `docs/plans/README.md` first. Prerequisite: Phase 3 merged (generations
being stored with dedup).

## Goal

Build the query layer that Phase 5 (API) and Phase 6 (UI) consume. All
aggregation happens in SQL. After this phase, every dashboard question is
answerable by one repository method, covered by tests seeded from the Phase 1
fixtures.

## Steps

### 1. Query filters — `internal/storage/usage.go`

One filter type used everywhere:

```go
type Filter struct {
    From      time.Time // inclusive, unix ms in SQL
    To        time.Time // exclusive
    Source    string    // "" = all
    Provider  string    // "" = all
    Model     string    // "" = all
}
```

Validate/normalize in one place (To zero → now; To < From → error; values
trimmed). Model/provider filters match raw stored values (exact match — the
UI offers dropdowns from the listing queries, so exact match is fine and
avoids LIKE injection complexity).

### 2. Aggregation queries — `internal/storage/queries.go`

All methods take `(ctx, Filter)` and return typed structs. Timezone handling:
all bucketing is done in **UTC day boundaries** in v1; note this in the API
docs (Phase 5) so the UI can label it. Buckets are computed as
`timestamp/86400000*86400000` (daily) — integer math, no SQLite datetime
functions (keeps everything index-friendly and unit-testable).

1. `Summary(ctx, f)` → totals for the filter range:
   `requests` (COUNT), SUMs of the five token columns, `cost_known_count`
   (COUNT WHERE cost IS NOT NULL), `cost_total` (SUM(cost), NULL if
   `cost_known_count == 0`), `cost_unknown_count` (COUNT WHERE cost IS NULL).
   **Critical rule: never let SQL turn "no cost data" into 0.** The struct
   uses `*float64` for CostTotal; the API serializes null.
2. `Timeseries(ctx, f, bucket)` (bucket ∈ day|week|month) → []{BucketStart
   int64, Requests, five token SUMs, CostTotal *float64, CostKnownCount}.
   Weekly buckets: Monday-anchored (compute anchor in Go, bucket in SQL via
   range BETWEEN — or precompute bucket edges in Go and GROUP BY a
   case-range; pick the simple correct option and test it).
   Monthly buckets: bucket edges computed in Go (calendar months, UTC),
   passed as a VALUES join or filtered per-month UNION — keep it simple,
   correctness over cleverness.
3. `BySource(ctx, f)` / `ByProvider(ctx, f)` / `ByModel(ctx, f)` →
   []{Key, Requests, token SUMs, CostTotal *float64, CostKnownCount,
   CostUnknownCount}. `ByModel` sorted by total tokens desc; expose the
   display-provider derivation later in the UI layer, not SQL.
4. `DistinctSources/Providers/Models(ctx, f)` → []string for filter
   dropdowns.
5. `RecentGenerations(ctx, f, limit, offset)` → []Generation (all columns),
   ordered by timestamp DESC — for the recent-requests table.
6. `GenerationByID(ctx, id)` → (*Generation, bool) for the detail view.

All queries: parameterized SQL; `COALESCE` only where a default is
semantically right (e.g. `SUM(input_tokens)` over rows where the column can
be NULL is fine — SUM ignores NULLs and returns NULL only when there are no
rows, which we handle via the presence of Requests).

Cost semantics (used by every cost-bearing method): rows with `cost IS NULL`
are not an error state; they are "source didn't report cost" (Copilot).
`cost_total` must only aggregate rows that have cost, and the response must
always tell the caller how many rows were cost-known vs unknown so the UI can
render "—". Include both counts in every cost-bearing response struct.

### 3. Test seeds — `internal/storage/queries_test.go`

- A seed helper inserting a fixed mix (in a temp DB): ~20 generations
  covering — copilot rows (cost NULL, various models incl. legacy-reasoning
  case), opencode rows (cost set, cache_creation set), a multi-round trace
  (2 chat spans same trace), one row with only input tokens set (sparse),
  rows on multiple days/months (including a month boundary and a leap-day
  edge if cheap to add), one dedup case.
- Table-driven tests per method asserting exact numbers, especially:
  - Summary with mixed cost: CostTotal == sum of opencode costs only,
    CostKnownCount/CostUnknownCount correct.
  - Summary over only-copilot rows: CostTotal is nil, not 0.
  - Timeseries day buckets land on UTC midnight edges.
  - ByModel ordering + sparse-token SUMs ignore NULLs.
  - RecentGenerations pagination (limit/offset, newest first).
- Migration continuity test: run Phase 2 migrations, insert via
  `InsertGeneration`, query — proves insert/query column agreement.

## Acceptance criteria

- [ ] All query methods implemented with tests over the seed set; exact
      expected values written in the tests (computed by hand, not by running
      the code — sanity-check at review).
- [ ] No cost-bearing method can report 0 for an all-unknown cost set; tests
      prove it stays null.
- [ ] Bucket edge cases (UTC midnight, week anchor, month boundary) tested.
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `gofmt -l .`
      empty.

## Do NOT

- No new tables (no rollups, no pre-aggregation) — SQLite handles this
  scale; a personal dashboard has thousands of rows, not millions.
- No retention/deletion logic.
- Do not put presentation concerns (formatting, "—") in this layer.
- Do not invent a pricing join. If you feel the urge, re-read the README
  guardrails.

## Report back

Method list with signatures, the seed set composition, one sentence on how
weekly/monthly bucketing is implemented, and the cost-null test proof.
