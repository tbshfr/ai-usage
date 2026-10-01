package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

var rollupDay0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func newRollupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

// rollupRows generates a deterministic mix over rollupDay0-3 .. +3 days:
// several sources (including OpenAI-style token counting and Copilot
// helper agents), missing provider/model/token/conversation, a
// conversation spanning every day, harness costs and rows left for
// estimation, at day edges and mid-day.
func rollupRows(tokens []int64) []normalize.Generation {
	sources := []struct{ source, provider, model, agent string }{
		{"claude-code", "anthropic", "claude-opus-5-5", ""},
		{"codex", "openai", "gpt-6.1-sol", ""},
		{"copilot", "github", "gpt-6-sol", "agent"},
		{"copilot", "github", "gpt-6-sol", normalize.AgentXtabProvider},
		{"copilot", "", "", normalize.AgentTitle},
		{"opencode", "openrouter", "moonshotai/kimi-k3", "build"},
	}
	offsets := []time.Duration{0, 5 * time.Hour, 12*time.Hour + 30*time.Minute, 24*time.Hour - time.Millisecond}
	var out []normalize.Generation
	n := 0
	for d := -3; d <= 3; d++ {
		for _, off := range offsets {
			for i, s := range sources {
				n++
				g := normalize.Generation{
					ID:                  fmt.Sprintf("g%d", n),
					Timestamp:           rollupDay0.AddDate(0, 0, d).Add(off),
					Source:              s.source,
					Provider:            s.provider,
					Model:               s.model,
					InputTokens:         ptrInt(int64(1000 + n)),
					OutputTokens:        ptrInt(int64(300 + n)),
					CacheReadTokens:     ptrInt(int64(200 * (n % 3))),
					CacheCreationTokens: ptrInt(int64(10 * (n % 4))),
					AgentName:           s.agent,
					ConversationID:      fmt.Sprintf("conv-%d-%d", d, i),
				}
				switch {
				case s.source == "claude-code":
					g.ConversationID = "conv-long"
				case s.source == "opencode" && n%3 == 0:
					g.ConversationID = ""
				}
				if n%5 != 0 {
					g.ReasoningTokens = ptrInt(int64(n % 70))
				}
				if s.source == "claude-code" || s.source == "opencode" {
					c := float64(n) / 1000
					g.Cost = &c
				}
				if n%4 != 0 {
					g.TokenID = tokens[n%len(tokens)]
				}
				out = append(out, g)
			}
		}
	}
	return out
}

func ptrInt(v int64) *int64 { return &v }

// scanRows reads every row as values, keeping floats for tolerant
// comparison.
func scanRows(t *testing.T, db *sql.DB, q string, args ...any) [][]any {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalRows(a, b [][]any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			x, xf := a[i][j].(float64)
			y, yf := b[i][j].(float64)
			if xf && yf {
				if math.Abs(x-y) > 1e-9 {
					return false
				}
			} else if fmt.Sprint(a[i][j]) != fmt.Sprint(b[i][j]) {
				return false
			}
		}
	}
	return true
}

func orderedByKey(r rollupSpec, q string) string {
	order := make([]string, len(r.keys)+1)
	for i := range order {
		order[i] = fmt.Sprint(i + 1)
	}
	return q + "\nORDER BY " + strings.Join(order, ", ")
}

// groupedFacts merges facts per rollup key; every aggregate query is a
// coarser grouping of this, so equal results here imply equal results for
// the queries built on r.
func groupedFacts(r rollupSpec, facts string) string {
	return orderedByKey(r, r.groupSQL(facts))
}

func rawFacts(r rollupSpec, where string) string {
	return "(" + r.rawFactsSQL(dayBucketSQL, where) + ")"
}

func assertRollupInSync(t *testing.T, db *sql.DB, step string) {
	t.Helper()
	for _, r := range rollups {
		got := scanRows(t, db, orderedByKey(r, `SELECT day, `+strings.Join(r.columns(), ", ")+` FROM `+r.table))
		want := scanRows(t, db, groupedFacts(r, rawFacts(r, "true")))
		if !equalRows(got, want) {
			t.Fatalf("%s: %s out of sync\n got %v\nwant %v", step, r.table, got, want)
		}
		requests := len(r.keys) + 1
		for _, row := range got {
			if row[requests].(int64) <= 0 {
				t.Fatalf("%s: %s keeps an empty group %v", step, r.table, row)
			}
		}
	}
}

func TestRollupTracksWrites(t *testing.T) {
	ctx := context.Background()
	db := newRollupDB(t)
	work, err := CreateToken(ctx, db, "laptop", "work", sha256.Sum256([]byte("a")), "aa")
	if err != nil {
		t.Fatal(err)
	}
	home, err := CreateToken(ctx, db, "desktop", "", sha256.Sum256([]byte("b")), "bb")
	if err != nil {
		t.Fatal(err)
	}
	gens := rollupRows([]int64{work, home})
	if _, err := InsertGenerations(ctx, db, gens); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "insert")

	// Merges: an earlier timestamp moves a row to the previous day, and
	// NULL model/provider/token/cost get filled, changing the row's key.
	var merges []normalize.Generation
	for _, g := range gens[:40] {
		m := g
		m.Timestamp = g.Timestamp.Add(-6 * time.Hour)
		m.Provider, m.Model, m.TokenID = "github", "gpt-6-sol", home
		m.ConversationID = "conv-merged"
		if g.Cost == nil {
			c := 0.25
			m.Cost = &c
		}
		merges = append(merges, m)
	}
	if _, err := InsertGenerations(ctx, db, merges); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "merge")

	pending, err := PendingPricing(ctx, db, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("expected rows pending pricing")
	}
	for i, g := range pending {
		c := float64(i) / 100
		g.Cost, g.CostSource = &c, "openrouter"
		if i%3 == 0 {
			g.Cost, g.CostSource = new(float64), "free"
		}
		if _, err := ApplyPricing(ctx, db, g); err != nil {
			t.Fatal(err)
		}
	}
	assertRollupInSync(t, db, "pricing")

	// The first configured token adopts unauthenticated rows; clear the
	// table so SeedTokenID takes its backfill path.
	if _, err := db.Exec(`DELETE FROM api_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedTokenID(ctx, db, sha256.Sum256([]byte("c")), "cc"); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "token backfill")

	if _, err := db.Exec(`DELETE FROM generations WHERE source = 'codex' OR id IN ('g1', 'g2', 'g3')`); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "delete")

	if _, err := db.Exec(`DELETE FROM generations`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rollups {
		var left int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + r.table).Scan(&left); err != nil || left != 0 {
			t.Fatalf("%s rows after deleting everything = %d, %v", r.table, left, err)
		}
	}
}

// TestFactsMatchRawRows checks the rollup/raw split of factsSQL against
// facts computed from raw rows only, for both rollups, ranges with and
// without partial days, and each dimension filter.
func TestFactsMatchRawRows(t *testing.T) {
	ctx := context.Background()
	db := newRollupDB(t)
	work, err := CreateToken(ctx, db, "laptop", "work", sha256.Sum256([]byte("a")), "aa")
	if err != nil {
		t.Fatal(err)
	}
	home, err := CreateToken(ctx, db, "desktop", "", sha256.Sum256([]byte("b")), "bb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertGenerations(ctx, db, rollupRows([]int64{work, home})); err != nil {
		t.Fatal(err)
	}

	d := func(days int, off time.Duration) time.Time { return rollupDay0.AddDate(0, 0, days).Add(off) }
	ranges := []struct {
		name     string
		from, to time.Time
	}{
		{"open start, aligned end", time.Time{}, d(0, 0)},
		{"open start, mid-day end", time.Time{}, d(2, 5*time.Hour)},
		{"open start, default end", time.Time{}, time.Time{}},
		{"aligned", d(-2, 0), d(2, 0)},
		{"partial days on both ends", d(-2, 3*time.Hour), d(1, 12*time.Hour+30*time.Minute)},
		{"edges one ms off a day", d(0, -time.Millisecond), d(1, time.Millisecond)},
		{"within one day", d(1, time.Hour), d(1, 13*time.Hour)},
		{"across midnight, no whole day", d(0, 12*time.Hour), d(1, 6*time.Hour)},
		{"from only", d(-1, 7*time.Hour), time.Time{}},
	}
	dims := []struct {
		name string
		f    Filter
	}{
		{"all", Filter{}},
		{"source", Filter{Source: "copilot"}},
		{"provider", Filter{Provider: "openrouter"}},
		{"model", Filter{Model: "gpt-6-sol"}},
		{"no token", Filter{Token: TokenNone}},
		{"token", Filter{Token: fmt.Sprint(work)}},
		{"group", Filter{Group: "work"}},
		{"ungrouped", Filter{Ungrouped: true}},
	}
	for _, r := range ranges {
		for _, dim := range dims {
			f := dim.f
			f.From, f.To = r.from, r.to
			nf, err := f.normalize(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			where, rawArgs := nf.whereSQL()
			for _, rs := range rollups {
				facts, args := nf.factsSQL(rs, false)
				got := scanRows(t, db, groupedFacts(rs, facts), args...)
				want := scanRows(t, db, groupedFacts(rs, rawFacts(rs, where)), rawArgs...)
				if len(want) == 0 {
					t.Fatalf("%s/%s: fixture selects no rows", r.name, dim.name)
				}
				if !equalRows(got, want) {
					t.Errorf("%s/%s/%s:\n got %v\nwant %v", rs.table, r.name, dim.name, got, want)
				}
			}
		}
	}
}

func TestMigrateRebuildsStaleRollup(t *testing.T) {
	ctx := context.Background()
	db := newRollupDB(t)
	gens := rollupRows([]int64{0})
	if _, err := InsertGenerations(ctx, db, gens[:50]); err != nil {
		t.Fatal(err)
	}
	// Simulate rollups built by an older definition: wrong contents, an
	// outdated trigger set, and a different hash.
	for _, q := range []string{
		`UPDATE usage_daily SET requests = requests + 7`,
		`UPDATE conversation_daily SET first_ts = 0`,
		`DROP TRIGGER generations_rollup_insert`,
		`UPDATE rollup_state SET definition_hash = 'old'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "rebuild")
	if _, err := InsertGenerations(ctx, db, gens[50:]); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "insert after rebuild")

	var triggers []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	if want := []string{"generations_rollup_delete", "generations_rollup_insert", "generations_rollup_update"}; !reflect.DeepEqual(triggers, want) {
		t.Errorf("triggers = %v, want %v", triggers, want)
	}
}

// TestMigrateReinstallsMissingTriggers covers triggers lost while the
// definition hash still matches, e.g. by a migration rebuilding
// generations: the rollups missed writes and must be rebuilt.
func TestMigrateReinstallsMissingTriggers(t *testing.T) {
	ctx := context.Background()
	db := newRollupDB(t)
	gens := rollupRows([]int64{0})
	if _, err := InsertGenerations(ctx, db, gens[:50]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER generations_rollup_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertGenerations(ctx, db, gens[50:]); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "rebuild")
	if _, err := db.Exec(`DELETE FROM generations WHERE id IN ('g1', 'g60')`); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "delete after rebuild")
}

// TestRollupSpanKeyLikeConversationID covers a conversation ID equal to a
// per-day group key: its group also holds session-less rows, so the span
// recompute must not narrow to rows with that conversation ID.
func TestRollupSpanKeyLikeConversationID(t *testing.T) {
	ctx := context.Background()
	db := newRollupDB(t)
	gen := func(id string, off time.Duration, conv string) normalize.Generation {
		return normalize.Generation{ID: id, Timestamp: rollupDay0.Add(off), Source: "codex",
			Provider: "openai", Model: "gpt-6.1-sol", ConversationID: conv, InputTokens: ptrInt(10)}
	}
	key := fmt.Sprintf("%s%d", convKeyOtherPrefix, rollupDay0.UnixMilli())
	if _, err := InsertGenerations(ctx, db, []normalize.Generation{
		gen("mimic-first", time.Hour, key),
		gen("plain", 2*time.Hour, ""),
		gen("mimic-last", 3*time.Hour, key),
	}); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "insert")
	if _, err := db.Exec(`UPDATE generations SET timestamp = timestamp + 1000 WHERE id = 'mimic-first'`); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "move first")
	if _, err := db.Exec(`DELETE FROM generations WHERE id IN ('mimic-first', 'mimic-last')`); err != nil {
		t.Fatal(err)
	}
	assertRollupInSync(t, db, "delete")
}

// TestRollupWritesScaleLinearly guards against triggers whose cost grows
// with the rows or groups of a day: updating every row of a day with many
// groups must take time roughly linear in the row count (4x rows, well
// under 16x time). The rows mix conversations of two rows half the rows
// apart, one long conversation, a shared session-less group, and session-less rows split
// into per-day groups by model, whose span recompute walks the whole day.
func TestRollupWritesScaleLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	ctx := context.Background()
	run := func(n int) map[string]time.Duration {
		db := newRollupDB(t)
		gens := make([]normalize.Generation, n)
		for i := range gens {
			gens[i] = normalize.Generation{ID: fmt.Sprintf("s%d", i), Timestamp: rollupDay0.Add(time.Duration(i) * time.Second),
				Source: "claude-code", Provider: "anthropic", Model: "claude-opus-5-5",
				ConversationID: fmt.Sprintf("conv-%d", i/4%(n/8)), InputTokens: ptrInt(10)}
			switch i % 4 {
			case 1:
				gens[i].ConversationID, gens[i].Model = "", fmt.Sprintf("model-%d", i)
			case 2:
				gens[i].ConversationID = ""
			case 3:
				gens[i].ConversationID = "conv-long"
			}
		}
		if _, err := InsertGenerations(ctx, db, gens); err != nil {
			t.Fatal(err)
		}
		merged := append([]normalize.Generation(nil), gens...)
		for i := range merged {
			c := 0.01
			merged[i].Cost = &c
		}
		// A merge filling in harness costs, a pricing-style cost update,
		// the first token's backfill (removing each group's rows in
		// order), and a timestamp change of the conversations.
		writes := []struct {
			name string
			do   func() error
		}{
			{"merge", func() error { _, err := InsertGenerations(ctx, db, merged); return err }},
			{"cost update", func() error {
				_, err := db.Exec(`UPDATE generations SET cost = 0.02, cost_source = 'openrouter'`)
				return err
			}},
			{"token backfill", func() error {
				_, err := SeedTokenID(ctx, db, sha256.Sum256([]byte("seed")), "ed")
				return err
			}},
			{"timestamp change", func() error {
				_, err := db.Exec(`UPDATE generations SET timestamp = timestamp - 1 WHERE conversation_id IS NOT NULL`)
				return err
			}},
			{"delete", func() error { _, err := db.Exec(`DELETE FROM generations`); return err }},
		}
		took := make(map[string]time.Duration)
		for _, w := range writes {
			start := time.Now()
			if err := w.do(); err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			took[w.name] = time.Since(start)
		}
		return took
	}
	small, large := run(1000), run(4000)
	for name, d := range large {
		if ratio := float64(d) / float64(small[name]); ratio > 8 {
			t.Errorf("%s: 4x rows took %.1fx as long (%v vs %v)", name, ratio, d, small[name])
		}
	}
}
