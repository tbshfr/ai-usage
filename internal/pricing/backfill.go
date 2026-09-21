package pricing

import (
	"context"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// backfill runs once per database using the first available fresh catalog.
// Unknown models remain unknown after completion; later catalog refreshes do
// not revisit history. The marker is written only after all batches succeed.
// To retire this job, remove this file, backfill_test.go, storage/pricing_backfill.go, the call
// in service.go, and its backfillDone field. Keep migration 0005 for upgrades.
func (s *Service) backfill(ctx context.Context) (bool, error) {
	done, err := storage.PricingBackfillComplete(ctx, s.db)
	if err != nil {
		return false, err
	}
	if done {
		s.backfillDone = true
		return false, nil
	}
	changed := false
	after := ""
	for {
		gens, err := storage.MissingHistoricalCosts(ctx, s.db, after)
		if err != nil {
			return changed, err
		}
		if len(gens) == 0 {
			break
		}
		for _, g := range gens {
			after = g.ID
			updated, err := storage.ApplyPricing(ctx, s.db, s.enrich(g))
			if err != nil {
				return changed, err
			}
			changed = changed || updated
		}
	}
	if err := storage.CompletePricingBackfill(ctx, s.db, s.now()); err != nil {
		return changed, err
	}
	s.backfillDone = true
	return changed, nil
}
