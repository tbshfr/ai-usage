package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UpsertDailySnapshot writes one day's counters and per-reason breakdown
// atomically: the stats_daily row and the stats_daily_reasons rows commit
// in a single transaction. The two-table split previously used separate
// transactions, so a crash between them left reason rows without a
// stats_daily row — an orphan old day the dashboard never displayed
// (loadStats iterates stats_daily and only uses reason days as a
// keep-filter). Callers must always persist both halves together via this
// function instead of calling UpsertDailyStats/UpsertDailyReasons in
// sequence.
func UpsertDailySnapshot(ctx context.Context, db *sql.DB, s DailyStats, reasons []ReasonStat) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert daily snapshot %s: begin: %w", s.Day, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM stats_daily_reasons WHERE day = ?`, s.Day); err != nil {
		return fmt.Errorf("upsert daily snapshot %s: clear reasons: %w", s.Day, err)
	}
	for _, r := range reasons {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stats_daily_reasons (day, kind, reason, count) VALUES (?, ?, ?, ?)`,
			s.Day, r.Kind, r.Reason, r.Count,
		); err != nil {
			return fmt.Errorf("upsert daily snapshot %s: insert %s/%s: %w", s.Day, r.Kind, r.Reason, err)
		}
	}
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
	if _, err := tx.ExecContext(ctx, q,
		s.Day, s.Received, s.Normalized, s.Stored, s.Deduplicated, s.Rejected,
		s.IgnoredNotUsed, s.NormalizationErrors, s.IngestionErrors,
		time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("upsert daily snapshot %s: upsert stats: %w", s.Day, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert daily snapshot %s: commit: %w", s.Day, err)
	}
	return nil
}
