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

// ApplyPricing uses a revision check so enrichment cannot overwrite newer telemetry.
func ApplyPricing(ctx context.Context, db *sql.DB, g normalize.Generation) (bool, error) {
	var fetched any
	if g.PricingFetchedAt != nil {
		fetched = g.PricingFetchedAt.UnixMilli()
	}
	result, err := db.ExecContext(ctx, `UPDATE generations SET cost = ?, cost_source = ?,
  pricing_model_id = ?, pricing_fetched_at = ?, pricing_rates = ?, pricing_pending = 0
  WHERE id = ? AND pricing_revision = ? AND cost_reported_by_harness = 0`,
		nullableFloat(g.Cost), g.CostSource, nullableString(g.PricingModelID), fetched, nullableString(g.PricingRates), g.ID, g.PricingRevision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0 && g.Cost != nil, err
}
