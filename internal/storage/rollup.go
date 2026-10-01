package storage

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// Aggregate queries read "facts": rows of per-bucket partial sums. Over long
// ranges most facts come from per-UTC-day rollup tables kept in sync by
// triggers, so a year of history costs a few thousand rollup rows instead
// of every generation. Partial days at the edges of a range, conversation
// filters (not part of the rollup keys), and hourly buckets read
// generations directly through the same expressions.
//
//   - usage_daily backs summaries, charts, breakdowns, and the leaderboard.
//   - conversation_daily backs the sessions list; it also tracks each
//     group's first and last timestamp.

var factMeasures = []string{
	"requests", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
	"reasoning_tokens", "cost_reported", "cost_estimated", "cost_free", "cost_known", "cost_sum",
}

const (
	dayBucketSQL  = "timestamp / 86400000 * 86400000"
	hourBucketSQL = "timestamp / 3600000 * 3600000"
)

// factDimsSQL derives the filterable dimensions of a generation. Missing
// provider/model/token become ”/”/0 so they can key a rollup.
const factDimsSQL = `source,
	COALESCE(provider, '') AS provider,
	COALESCE(model, '') AS model,
	COALESCE(token_id, 0) AS token_id`

// factHelperSQL marks the session-less Copilot agents excluded from active
// days.
const factHelperSQL = `(source = '` + normalize.SourceCopilot + `' AND COALESCE(agent_name, '') IN ('` +
	normalize.AgentXtabProvider + `', '` + normalize.AgentTitle + `', '` + normalize.AgentProgressMessages + `')) AS helper`

const factMeasuresSQL = `1 AS requests,
	` + uncachedInputSQL + ` AS input_tokens,
	` + outputTokensSQL + ` AS output_tokens,
	COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
	COALESCE(cache_creation_tokens, 0) AS cache_creation_tokens,
	COALESCE(reasoning_tokens, 0) AS reasoning_tokens,
	(cost_source = 'harness' AND cost IS NOT NULL) AS cost_reported,
	(cost_source IN ('openrouter', 'manual') AND cost IS NOT NULL) AS cost_estimated,
	(cost_source = 'free' AND cost IS NOT NULL) AS cost_free,
	(cost IS NOT NULL) AS cost_known,
	COALESCE(cost, 0) AS cost_sum`

// factInputColumns are the generations columns the fact expressions read;
// changing any of them moves a row's rollup contributions.
var factInputColumns = []string{
	"timestamp", "source", "provider", "model", "token_id", "agent_name", "conversation_id",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
	"reasoning_tokens", "cost", "cost_source",
}

// Aggregates over facts. factCostSQL is NULL when no row has a known cost
// ("no cost data" must never surface as 0).
const (
	factSumsSQL = `COALESCE(SUM(requests), 0),
	COALESCE(SUM(input_tokens), 0),
	COALESCE(SUM(output_tokens), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_creation_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0),
	COALESCE(SUM(cost_reported), 0),
	COALESCE(SUM(cost_estimated), 0),
	COALESCE(SUM(cost_free), 0),
	COALESCE(SUM(cost_known), 0)`
	factCostSQL        = `CASE WHEN SUM(cost_known) > 0 THEN SUM(cost_sum) END`
	factUnknownSQL     = `COALESCE(SUM(requests) - SUM(cost_known), 0)`
	factTotalTokensSQL = `COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens
	+ cache_creation_tokens + reasoning_tokens), 0)`
)

// rollupSpec describes one rollup table: day plus keys form its primary key.
type rollupSpec struct {
	table string
	// values is the per-generation select list after bucket; it yields
	// columns() in order.
	values string
	keys   []string
	span   bool
	// spanScope optionally narrows the span recompute of row's group
	// (facts aliased o): when guard holds, every generation of the group
	// matches cond, which an index can serve. Other groups scan the day.
	spanScope func(row string) (guard, cond string)
}

var usageRollup = rollupSpec{
	table:  "usage_daily",
	values: factDimsSQL + ",\n\t" + factHelperSQL + ",\n\t" + factMeasuresSQL,
	keys:   []string{"source", "provider", "model", "token_id", "helper"},
}

var conversationRollup = rollupSpec{
	table: "conversation_daily",
	values: conversationKeySQL + " AS conversation,\n\t" + factDimsSQL + ",\n\t" + factMeasuresSQL +
		",\n\ttimestamp AS first_ts, timestamp AS last_ts",
	keys: []string{"conversation", "source", "provider", "model", "token_id"},
	span: true,
	// A group keyed by its own conversation ID holds only rows with that
	// ID, unless the ID mimics a per-day key, whose group also takes
	// session-less rows.
	spanScope: func(row string) (string, string) {
		guard := "o.conversation IS " + row + ".conversation_id"
		for _, p := range convKeyPrefixes {
			guard += " AND o.conversation NOT GLOB '" + p + "*'"
		}
		return guard, "conversation_id = " + row + ".conversation_id"
	},
}

var rollups = []rollupSpec{usageRollup, conversationRollup}

func (r rollupSpec) columns() []string {
	cols := append(append([]string{}, r.keys...), factMeasures...)
	if r.span {
		cols = append(cols, "first_ts", "last_ts")
	}
	return cols
}

func (r rollupSpec) rawFactsSQL(bucket, where string) string {
	return `SELECT ` + bucket + ` AS bucket, ` + r.values + `
	FROM generations WHERE ` + where
}

func (r rollupSpec) groupSQL(facts string) string {
	group := append([]string{"bucket"}, r.keys...)
	sel := append([]string{}, group...)
	for _, m := range factMeasures {
		sel = append(sel, "SUM("+m+")")
	}
	if r.span {
		sel = append(sel, "MIN(first_ts)", "MAX(last_ts)")
	}
	return `SELECT ` + strings.Join(sel, ", ") + `
	FROM ` + facts + `
	GROUP BY ` + strings.Join(group, ", ")
}

// factsSQL returns a parenthesized subquery of facts for the normalized
// filter, with the columns bucket then r.columns(), bucketed by UTC day (or
// hour when hourly is set).
func (f Filter) factsSQL(r rollupSpec, hourly bool) (string, []any) {
	if hourly || f.Conversation != "" {
		bucket := dayBucketSQL
		if hourly {
			bucket = hourBucketSQL
		}
		where, args := f.whereSQL()
		return "(" + r.rawFactsSQL(bucket, where) + ")", args
	}

	rawDims, rawArgs := f.dimSQL(false)
	rollDims, rollArgs := f.dimSQL(true)
	var parts []string
	var args []any
	raw := func(from, to int64) {
		parts = append(parts, r.rawFactsSQL(dayBucketSQL, "timestamp >= ? AND timestamp < ?"+rawDims))
		args = append(append(args, from, to), rawArgs...)
	}
	rollup := func(cond string, bounds ...any) {
		parts = append(parts, `SELECT day AS bucket, `+strings.Join(r.columns(), ", ")+`
	FROM `+r.table+` WHERE `+cond+rollDims)
		args = append(append(args, bounds...), rollArgs...)
	}

	// Whole days in [lo, hi) come from the rollup, the rest from raw rows.
	to := f.To.UnixMilli()
	hi := to - to%dayMs
	if f.From.IsZero() {
		rollup("day < ?", hi)
	} else {
		from := f.From.UnixMilli()
		lo := from + (dayMs-from%dayMs)%dayMs
		if lo >= hi {
			raw(from, to)
			return "(" + parts[0] + ")", args
		}
		if from < lo {
			raw(from, lo)
		}
		rollup("day >= ? AND day < ?", lo, hi)
	}
	if hi < to {
		raw(hi, to)
	}
	return "(" + strings.Join(parts, "\nUNION ALL\n") + ")", args
}

func (r rollupSpec) rowFactsSQL(row string) string {
	cols := make([]string, len(factInputColumns))
	for i, c := range factInputColumns {
		cols[i] = row + "." + c + " AS " + c
	}
	return `(SELECT ` + dayBucketSQL + ` AS bucket, ` + r.values + `
	FROM (SELECT ` + strings.Join(cols, ", ") + `))`
}

// addSQL adds (sign "+") or removes (sign "-") one row's facts. Adding
// widens the group's span; removing leaves it to spanSQL.
func (r rollupSpec) addSQL(row, sign string) string {
	sel := append([]string{"bucket"}, r.keys...)
	update := make([]string, 0, len(factMeasures)+2)
	for _, m := range factMeasures {
		sel = append(sel, sign+m)
		update = append(update, m+" = "+m+" + excluded."+m)
	}
	if r.span {
		sel = append(sel, "first_ts", "last_ts")
		if sign == "+" {
			update = append(update, "first_ts = MIN(first_ts, excluded.first_ts)", "last_ts = MAX(last_ts, excluded.last_ts)")
		}
	}
	// WHERE true disambiguates INSERT ... SELECT from the upsert clause.
	return `INSERT INTO ` + r.table + ` (day, ` + strings.Join(r.columns(), ", ") + `)
	SELECT ` + strings.Join(sel, ", ") + `
	FROM ` + r.rowFactsSQL(row) + ` WHERE true
	ON CONFLICT (day, ` + strings.Join(r.keys, ", ") + `) DO UPDATE SET ` + strings.Join(update, ", ")
}

// pruneSQL drops row's group when removing row emptied it, so results
// never show zero-request groups. It looks up that one group by primary
// key: scanning the day would make each write cost O(groups per day).
func (r rollupSpec) pruneSQL(row string) string {
	return `DELETE FROM ` + r.table + ` WHERE requests = 0
	AND (day, ` + strings.Join(r.keys, ", ") + `) IN (SELECT bucket, ` + strings.Join(r.keys, ", ") + ` FROM ` + r.rowFactsSQL(row) + `)`
}

// spanSQL recomputes the span of row's group from its generations when the
// removed row was its first or last; generations already reflects the
// change when the trigger runs.
func (r rollupSpec) spanSQL(row string) []string {
	if r.spanScope == nil {
		return []string{r.spanStmtSQL(row, "true", "true")}
	}
	guard, cond := r.spanScope(row)
	return []string{
		r.spanStmtSQL(row, guard, cond),
		r.spanStmtSQL(row, "NOT ("+guard+")", "true"),
	}
}

// spanStmtSQL is one spanSQL statement for the groups matching guard,
// reading the day's generations matching cond. When the removed row was
// the group's first, every remaining row is later (and earlier when it was
// the last), so the new bound is the group's nearest row from the removed
// one: walking timestamp order from there skips only the rows in between,
// which keeps removing a group's rows in order (e.g. the token backfill)
// linear instead of re-reading the group per row.
func (r rollupSpec) spanStmtSQL(row, guard, cond string) string {
	day := strings.ReplaceAll(dayBucketSQL, "timestamp", row+".timestamp")
	match := make([]string, len(r.keys))
	own := make([]string, len(r.keys))
	for i, k := range r.keys {
		match[i] = "f." + k + " = o." + k
		own[i] = r.table + "." + k + " = o." + k
	}
	nearest := func(col, cmp, dir string) string {
		facts := `(` + r.rawFactsSQL(dayBucketSQL, cond+` AND timestamp `+cmp+` `+row+`.timestamp
		AND timestamp >= `+day+` AND timestamp < `+day+` + 86400000`) + `) AS f`
		return `CASE WHEN ` + row + `.timestamp = ` + r.table + `.` + col + ` THEN (SELECT f.` + col + ` FROM ` + facts + `
		WHERE ` + strings.Join(match, " AND ") + ` ORDER BY f.` + col + ` ` + dir + ` LIMIT 1) ELSE ` + r.table + `.` + col + ` END`
	}
	return `UPDATE ` + r.table + ` SET
	first_ts = ` + nearest("first_ts", ">=", "ASC") + `,
	last_ts = ` + nearest("last_ts", "<=", "DESC") + `
	FROM ` + r.rowFactsSQL(row) + ` AS o
	WHERE ` + r.table + `.day = o.bucket AND ` + strings.Join(own, " AND ") + `
	AND ` + row + `.timestamp IN (` + r.table + `.first_ts, ` + r.table + `.last_ts)
	AND ` + guard
}

var rollupTriggers = []string{"generations_rollup_insert", "generations_rollup_delete", "generations_rollup_update"}

func rollupDefinition() (triggers []string, backfills []string) {
	var onInsert, onDelete, onUpdate []string
	for _, r := range rollups {
		onInsert = append(onInsert, r.addSQL("NEW", "+"))
		onDelete = append(onDelete, r.addSQL("OLD", "-"), r.pruneSQL("OLD"))
		onUpdate = append(onUpdate, r.addSQL("OLD", "-"), r.addSQL("NEW", "+"), r.pruneSQL("OLD"))
		if r.span {
			onDelete = append(onDelete, r.spanSQL("OLD")...)
			onUpdate = append(onUpdate, r.spanSQL("OLD")...)
		}
		backfills = append(backfills, `INSERT INTO `+r.table+` (day, `+strings.Join(r.columns(), ", ")+`)
	`+r.groupSQL("("+r.rawFactsSQL(dayBucketSQL, "true")+")"))
	}
	body := func(stmts []string) string { return "BEGIN\n\t" + strings.Join(stmts, ";\n\t") + ";\nEND" }
	changed := make([]string, len(factInputColumns))
	for i, c := range factInputColumns {
		changed[i] = "OLD." + c + " IS NOT NEW." + c
	}
	triggers = []string{
		`CREATE TRIGGER ` + rollupTriggers[0] + ` AFTER INSERT ON generations ` + body(onInsert),
		`CREATE TRIGGER ` + rollupTriggers[1] + ` AFTER DELETE ON generations ` + body(onDelete),
		`CREATE TRIGGER ` + rollupTriggers[2] + ` AFTER UPDATE OF ` + strings.Join(factInputColumns, ", ") + ` ON generations
WHEN ` + strings.Join(changed, " OR ") + ` ` + body(onUpdate),
	}
	return triggers, backfills
}

const rollupStateName = "rollups"

// syncRollup installs the rollup triggers and rebuilds the rollups when
// the definition differs from the one they were built with (first run, or
// a release that changed the fact expressions), or when a trigger is
// missing: rebuilding generations (create, copy, drop, rename) drops its
// triggers, and the rollups have missed every write since.
func syncRollup(db *sql.DB, logger *slog.Logger) error {
	triggers, backfills := rollupDefinition()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(append(triggers, backfills...), "\n;\n"))))
	var stored string
	err := db.QueryRow(`SELECT definition_hash FROM rollup_state WHERE name = ?`, rollupStateName).Scan(&stored)
	switch {
	case err == nil && stored == hash:
		names := make([]any, len(rollupTriggers))
		for i, name := range rollupTriggers {
			names[i] = name
		}
		var installed int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'generations'
			AND name IN (?`+strings.Repeat(", ?", len(names)-1)+`)`, names...).Scan(&installed); err != nil {
			return fmt.Errorf("read rollup triggers: %w", err)
		}
		if installed == len(rollupTriggers) {
			return nil
		}
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read rollup state: %w", err)
	}

	if logger != nil {
		logger.Info("rebuilding usage rollups")
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("rebuild rollups: begin: %w", err)
	}
	defer tx.Rollback()
	var stmts []string
	for _, name := range rollupTriggers {
		stmts = append(stmts, `DROP TRIGGER IF EXISTS `+name)
	}
	for _, r := range rollups {
		stmts = append(stmts, `DELETE FROM `+r.table)
	}
	stmts = append(append(stmts, backfills...), triggers...)
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild rollups: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO rollup_state (name, definition_hash) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET definition_hash = excluded.definition_hash`, rollupStateName, hash); err != nil {
		return fmt.Errorf("rebuild rollups: record state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuild rollups: commit: %w", err)
	}
	if logger != nil {
		logger.Info("usage rollups rebuilt")
	}
	return nil
}
