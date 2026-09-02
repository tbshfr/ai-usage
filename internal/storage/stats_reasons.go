package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// ReasonStat is one (kind, reason) counter of the daily stats breakdown.
// Kind and reason come from a fixed enum (internal/ingest/pipeline.go);
// no free-form strings are ever written, so the table stays bounded.
type ReasonStat struct {
	Kind   string
	Reason string
	Count  int64
}

// UpsertDailyReasons writes one day's per-reason counters as an absolute
// snapshot: the existing rows for the day are replaced, so a save is
// idempotent and restart-safe exactly like UpsertDailyStats.
func UpsertDailyReasons(ctx context.Context, db *sql.DB, day string, stats []ReasonStat) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert daily reasons %s: begin: %w", day, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM stats_daily_reasons WHERE day = ?`, day); err != nil {
		return fmt.Errorf("upsert daily reasons %s: clear: %w", day, err)
	}
	for _, s := range stats {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stats_daily_reasons (day, kind, reason, count) VALUES (?, ?, ?, ?)`,
			day, s.Kind, s.Reason, s.Count,
		); err != nil {
			return fmt.Errorf("upsert daily reasons %s: insert %s/%s: %w", day, s.Kind, s.Reason, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert daily reasons %s: commit: %w", day, err)
	}
	return nil
}

// RecentReasonDays returns up to limit distinct days that have at least
// one non-zero reason counter, newest first. Days whose only activity was
// transport rejections (http_reject) have zero record counters, so the
// stats page uses this to keep those rows visible.
func RecentReasonDays(ctx context.Context, db *sql.DB, limit int) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT day FROM stats_daily_reasons WHERE count > 0 GROUP BY day ORDER BY day DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent reason days: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, fmt.Errorf("scan recent reason days: %w", err)
		}
		out[day] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent reason days: %w", err)
	}
	return out, nil
}

// DailyReasonsForDay loads one day's per-reason counters, ordered by kind
// then reason; found is false when nothing was recorded for that day.
func DailyReasonsForDay(ctx context.Context, db *sql.DB, day string) ([]ReasonStat, bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT kind, reason, count FROM stats_daily_reasons WHERE day = ? ORDER BY kind, reason`, day)
	if err != nil {
		return nil, false, fmt.Errorf("daily reasons %s: %w", day, err)
	}
	defer rows.Close()
	var out []ReasonStat
	for rows.Next() {
		var s ReasonStat
		if err := rows.Scan(&s.Kind, &s.Reason, &s.Count); err != nil {
			return nil, false, fmt.Errorf("scan daily reasons %s: %w", day, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate daily reasons %s: %w", day, err)
	}
	return out, len(out) > 0, nil
}
