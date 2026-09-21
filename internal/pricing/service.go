package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const catalogURL = "https://openrouter.ai/api/v1/models"
const cacheTTL = 24 * time.Hour
const retryDelay = 5 * time.Minute

// Service owns a single worker. Notify is safe to call concurrently after commits.
// Construction makes no network requests; Run resumes queued pricing at startup.
type Service struct {
	db           *sql.DB
	logger       *slog.Logger
	notify       func()
	wake         chan struct{}
	client       *http.Client
	url          string
	now          func() time.Time
	retryDelay   time.Duration
	manual       map[string]manualPrice
	catalog      catalog
	fetched      time.Time
	retryAt      time.Time
	loaded       bool
	backfillDone bool
}

func New(db *sql.DB, logger *slog.Logger, notify func()) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	manual, err := parseManualPrices(manualPricesJSON)
	if err != nil {
		panic(fmt.Sprintf("invalid bundled manual prices: %v", err))
	}
	return &Service{manual: manual, db: db, logger: logger, notify: notify, wake: make(chan struct{}, 1), client: &http.Client{Timeout: 10 * time.Second}, url: catalogURL, now: time.Now, retryDelay: retryDelay}
}

func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	// Recover queued rows after a restart while leaving an empty database idle.
	if pending, err := storage.HasPendingPricing(ctx, s.db); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("check pending pricing at startup", "error", err)
		}
	} else if pending {
		s.Notify()
	}
	var retryTimer *time.Timer
	var retry <-chan time.Time
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry:
			retry = nil
			s.Notify()
		case <-s.wake:
			if err := s.process(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("pricing enrichment failed", "error", err)
			}
			if retryTimer != nil {
				retryTimer.Stop()
				retryTimer = nil
				retry = nil
			}
			// A failed cold-cache fetch leaves paid rows pending. Wake again
			// when its cooldown expires, even if no more telemetry arrives.
			if ctx.Err() == nil && len(s.catalog) == 0 && !s.retryAt.IsZero() {
				pending, err := storage.HasPendingPricing(ctx, s.db)
				if err != nil {
					s.logger.Warn("check pending pricing for retry", "error", err)
				} else if pending {
					delay := s.retryAt.Sub(s.now())
					if delay < 0 {
						delay = 0
					}
					retryTimer = time.NewTimer(delay)
					retry = retryTimer.C
				}
			}
		}
	}
}

func (s *Service) refresh(ctx context.Context) error {
	if !s.loaded {
		body, fetched, err := storage.PricingCatalog(ctx, s.db)
		if err != nil {
			return err
		}
		if body != "" {
			if err := json.Unmarshal([]byte(body), &s.catalog); err != nil {
				return fmt.Errorf("read cached prices: %w", err)
			}
			s.fetched = fetched
		}
		s.loaded = true
	}
	now := s.now()
	if (!s.fetched.IsZero() && now.Before(s.fetched.Add(cacheTTL))) || now.Before(s.retryAt) {
		return nil
	}
	// Failure cooldown also coalesces failed requests during an outage.
	s.retryAt = now.Add(s.retryDelay)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog HTTP status %d", resp.StatusCode)
	}
	const maxBody = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("catalog exceeds size limit")
	}
	prices, err := parseCatalog(body)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(prices)
	if err != nil {
		return err
	}
	fetched := s.now().UTC()
	if err := storage.SavePricingCatalog(ctx, s.db, string(encoded), fetched); err != nil {
		return err
	}
	s.catalog, s.fetched, s.retryAt = prices, fetched, time.Time{}
	return nil
}

func (s *Service) enrich(g normalize.Generation) normalize.Generation {
	if g.CostReportedByHarness {
		return g
	}
	if freeModel(g.Model) {
		zero := 0.0
		g.Cost, g.CostSource = &zero, "free"
		return g
	}
	original := g
	origin := "openrouter"
	var r rates
	if g.PricingRates != "" {
		if g.CostSource == "manual" {
			origin = "manual"
		}
		// Existing estimates retain their original rates when telemetry is enriched.
		if json.Unmarshal([]byte(g.PricingRates), &r) != nil {
			return g
		}
	} else {
		id, matched, ok := s.catalog.match(g.Model)
		stamp := s.fetched
		if !ok {
			id = strings.TrimSpace(g.Model)
			manual, found := s.manual[id]
			if !found {
				return g
			}
			matched, stamp, origin = manual.Rates, manual.UpdatedAt, "manual"
		}
		r = matched
		g.PricingModelID = id
		g.PricingFetchedAt = &stamp
		encoded, _ := json.Marshal(r)
		g.PricingRates = string(encoded)
	}
	if cost := estimate(g, r); cost != nil {
		g.Cost, g.CostSource = cost, origin
	} else {
		return original
	}
	return g
}

func (s *Service) process(ctx context.Context) error {
	if err := s.refresh(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.logger.Warn("pricing catalog unavailable; using cached prices when available", "error", err)
	}
	changed := false
	defer func() {
		if changed && s.notify != nil {
			s.notify()
		}
	}()
	after := ""
	for {
		gens, err := storage.PendingPricing(ctx, s.db, after)
		if err != nil {
			return err
		}
		if len(gens) == 0 {
			break
		}
		for _, g := range gens {
			after = g.ID
			// Keep pending paid records during a cold-cache outage for the
			// next attempt. Once any catalog is available, unknown models are
			// finalized as unknown: later refreshes do not revisit them, so
			// the pending queue cannot grow without bound (newly listed
			// models only price requests ingested after they appear).
			enriched := s.enrich(g)
			if len(s.catalog) == 0 && enriched.Cost == nil {
				continue
			}
			updated, err := storage.ApplyPricing(ctx, s.db, enriched)
			if err != nil {
				return err
			}
			changed = changed || updated
		}
	}
	// The one-time historical job is isolated in backfill.go and can be removed
	// along with this call after deployments have completed it.
	if !s.backfillDone && !s.fetched.IsZero() && s.now().Before(s.fetched.Add(cacheTTL)) {
		updated, err := s.backfill(ctx)
		changed = changed || updated
		if err != nil {
			return err
		}
	}
	return nil
}
