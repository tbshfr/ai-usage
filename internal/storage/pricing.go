package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

func PricingCatalog(ctx context.Context, db *sql.DB) (string, time.Time, error) {
	var body string
	var stamp int64
	err := db.QueryRowContext(ctx, `SELECT models_json, fetched_at FROM pricing_catalog WHERE singleton = 1`).Scan(&body, &stamp)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	return body, time.UnixMilli(stamp).UTC(), err
}

func SavePricingCatalog(ctx context.Context, db *sql.DB, body string, fetched time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO pricing_catalog VALUES (1, ?, ?) ON CONFLICT(singleton) DO UPDATE SET fetched_at = excluded.fetched_at, models_json = excluded.models_json`, fetched.UnixMilli(), body)
	return err
}

// PendingPricing uses keyset pagination and releases the reader before any writes.
func PendingPricing(ctx context.Context, db *sql.DB, after string) ([]normalize.Generation, error) {
	return pricingRows(ctx, db, `pricing_pending = 1 AND id > ?`, after)
}

// HasPendingPricing supports startup recovery and retry scheduling without
// loading a batch of generations.
func HasPendingPricing(ctx context.Context, db *sql.DB) (bool, error) {
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generations WHERE pricing_pending = 1)`).Scan(&pending)
	return pending, err
}

func pricingRows(ctx context.Context, db *sql.DB, where string, args ...any) ([]normalize.Generation, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+generationColumns+` FROM generations WHERE `+where+` ORDER BY id LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []normalize.Generation
	for rows.Next() {
		g, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// ApplyPricing saves a used rate set and updates its generation in one
// transaction. The revision check prevents stale enrichment from overwriting
// newer telemetry or leaving behind an unused snapshot.
func ApplyPricing(ctx context.Context, db *sql.DB, g normalize.Generation) (bool, error) {
	var fetched any
	if g.PricingFetchedAt != nil {
		fetched = g.PricingFetchedAt.UnixMilli()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var snapshotID any
	if g.PricingRates != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pricing_snapshots (rates_json) VALUES (?) ON CONFLICT(rates_json) DO NOTHING`, g.PricingRates); err != nil {
			return false, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM pricing_snapshots WHERE rates_json = ?`, g.PricingRates).Scan(&snapshotID); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE generations SET cost = ?, cost_source = ?,
  pricing_model_id = ?, pricing_fetched_at = ?, pricing_snapshot_id = ?, pricing_pending = 0
  WHERE id = ? AND pricing_revision = ? AND cost_reported_by_harness = 0`,
		nullableFloat(g.Cost), g.CostSource, nullableString(g.PricingModelID), fetched, snapshotID, g.ID, g.PricingRevision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return g.Cost != nil, nil
}
