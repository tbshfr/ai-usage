package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const catalogURL = "https://openrouter.ai/api/v1/models"
const cacheTTL = 24 * time.Hour
const retryDelay = 5 * time.Minute

// Service owns a single worker. Notify is safe to call concurrently after commits.
// Construction and startup never make network requests.
type Service struct {
	db           *sql.DB
	logger       *slog.Logger
	notify       func()
	wake         chan struct{}
	client       *http.Client
	url          string
	now          func() time.Time
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
	return &Service{db: db, logger: logger, notify: notify, wake: make(chan struct{}, 1), client: &http.Client{Timeout: 10 * time.Second}, url: catalogURL, now: time.Now}
}

func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			if err := s.process(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("pricing enrichment failed", "error", err)
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
	s.retryAt = now.Add(retryDelay)
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
	var r rates
	if g.PricingRates != "" {
		// Existing estimates retain their original rates when telemetry is enriched.
		if json.Unmarshal([]byte(g.PricingRates), &r) != nil {
			return g
		}
	} else {
		id, matched, ok := s.catalog.match(g.Model)
		if !ok {
			return g
		}
		r = matched
		g.PricingModelID = id
		stamp := s.fetched
		g.PricingFetchedAt = &stamp
		encoded, _ := json.Marshal(r)
		g.PricingRates = string(encoded)
	}
	if cost := estimate(g, r); cost != nil {
		g.Cost, g.CostSource = cost, "openrouter"
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
			// Keep pending paid records during a cold-cache outage for the next attempt.
			if len(s.catalog) == 0 && g.PricingRates == "" && !freeModel(g.Model) {
				continue
			}
			updated, err := storage.ApplyPricing(ctx, s.db, s.enrich(g))
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
