package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DailyStats is one UTC day's cumulative ingestion counters, persisted so
// they survive process restarts. Day is "YYYY-MM-DD" (UTC).
type DailyStats struct {
	Day                 string
	Received            int64
	Normalized          int64
	Stored              int64
	Deduplicated        int64
	Rejected            int64
	IgnoredNotUsed      int64
	NormalizationErrors int64
	IngestionErrors     int64
	UpdatedAt           time.Time
}

// UpsertDailyStats writes one day's counters as an absolute snapshot: the
// caller always persists the full day total (persisted base + this
// session's counters), so a save is idempotent and restart-safe.
func UpsertDailyStats(ctx context.Context, db *sql.DB, s DailyStats) error {
	const q = `INSERT INTO stats_daily (
		day, received, normalized, stored, deduplicated, rejected,
		ignored_not_used, normalization_errors, ingestion_errors, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(day) DO UPDATE SET
		received = excluded.received,
		normalized = excluded.normalized,
		stored = excluded.stored,
		deduplicated = excluded.deduplicated,
		rejected = excluded.rejected,
		ignored_not_used = excluded.ignored_not_used,
		normalization_errors = excluded.normalization_errors,
		ingestion_errors = excluded.ingestion_errors,
		updated_at = excluded.updated_at`
	_, err := db.ExecContext(ctx, q,
		s.Day, s.Received, s.Normalized, s.Stored, s.Deduplicated, s.Rejected,
		s.IgnoredNotUsed, s.NormalizationErrors, s.IngestionErrors,
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("upsert daily stats %s: %w", s.Day, err)
	}
	return nil
}

// DailyStatsForDay loads one day's counters; found is false when nothing
// was recorded for that day.
func DailyStatsForDay(ctx context.Context, db *sql.DB, day string) (DailyStats, bool, error) {
	row, err := scanDailyStats(db.QueryRowContext(ctx,
		`SELECT day, received, normalized, stored, deduplicated, rejected,
			ignored_not_used, normalization_errors, ingestion_errors, updated_at
		FROM stats_daily WHERE day = ?`, day))
	if err == sql.ErrNoRows {
		return DailyStats{}, false, nil
	}
	if err != nil {
		return DailyStats{}, false, fmt.Errorf("daily stats %s: %w", day, err)
	}
	return row, true, nil
}

// DailyStatsRange returns the days in [from, to] (inclusive, "YYYY-MM-DD"),
// newest first, at most limit days. An empty from or to is unbounded on
// that side; limit is clamped implicitly by the caller (always >= 1).
func DailyStatsRange(ctx context.Context, db *sql.DB, from, to string, limit int) ([]DailyStats, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT day, received, normalized, stored, deduplicated, rejected,
			ignored_not_used, normalization_errors, ingestion_errors, updated_at
		FROM stats_daily
		WHERE (? = '' OR day >= ?) AND (? = '' OR day <= ?)
		ORDER BY day DESC
		LIMIT ?`, from, from, to, to, limit)
	if err != nil {
		return nil, fmt.Errorf("daily stats range: %w", err)
	}
	return collectDailyStats(rows)
}

// RecentDailyStats returns up to limit days, newest first.
func RecentDailyStats(ctx context.Context, db *sql.DB, limit int) ([]DailyStats, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT day, received, normalized, stored, deduplicated, rejected,
			ignored_not_used, normalization_errors, ingestion_errors, updated_at
		FROM stats_daily
		ORDER BY day DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent daily stats: %w", err)
	}
	return collectDailyStats(rows)
}

func scanDailyStats(row *sql.Row) (DailyStats, error) {
	var s DailyStats
	var updated int64
	err := row.Scan(&s.Day, &s.Received, &s.Normalized, &s.Stored, &s.Deduplicated,
		&s.Rejected, &s.IgnoredNotUsed, &s.NormalizationErrors, &s.IngestionErrors, &updated)
	if err != nil {
		return DailyStats{}, err
	}
	s.UpdatedAt = time.UnixMilli(updated).UTC()
	return s, nil
}

func collectDailyStats(rows *sql.Rows) ([]DailyStats, error) {
	defer rows.Close()
	var out []DailyStats
	for rows.Next() {
		var s DailyStats
		var updated int64
		if err := rows.Scan(&s.Day, &s.Received, &s.Normalized, &s.Stored, &s.Deduplicated,
			&s.Rejected, &s.IgnoredNotUsed, &s.NormalizationErrors, &s.IngestionErrors, &updated); err != nil {
			return nil, fmt.Errorf("scan daily stats: %w", err)
		}
		s.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily stats: %w", err)
	}
	return out, nil
}
