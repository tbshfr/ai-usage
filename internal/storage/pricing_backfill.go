package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
)

// These helpers belong only to the removable one-time historical pricing job.
const pricingBackfillJob = "historical-costs-v1"

func PricingBackfillComplete(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pricing_jobs WHERE name = ?`, pricingBackfillJob).Scan(&n)
	return n > 0, err
}

func MissingHistoricalCosts(ctx context.Context, db *sql.DB, after string) ([]normalize.Generation, error) {
	return pricingRows(ctx, db, `cost IS NULL AND id > ?`, after)
}

func CompletePricingBackfill(ctx context.Context, db *sql.DB, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO pricing_jobs VALUES (?, ?)`, pricingBackfillJob, now.UnixMilli())
	return err
}
