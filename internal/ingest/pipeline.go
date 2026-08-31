package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const batchTimeout = 10 * time.Second

type Stats struct {
	Received            uint64
	Normalized          uint64
	Stored              uint64
	Deduplicated        uint64
	Rejected            uint64
	NormalizationErrors uint64
	IngestionErrors     uint64
}

// Pipeline routes decoded signals into normalized Generation records.
// Per-record failures never fail the batch; DB failures do (so exporters
// retry — dedup makes retries safe).
type Pipeline struct {
	db     *sql.DB
	logger *slog.Logger

	received   atomic.Uint64
	normalized atomic.Uint64
	stored     atomic.Uint64
	dedup      atomic.Uint64
	rejected   atomic.Uint64
	normErrors atomic.Uint64
	ingErrors  atomic.Uint64
}

func NewPipeline(db *sql.DB, logger *slog.Logger) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{db: db, logger: logger}
}

func (p *Pipeline) Stats() Stats {
	return Stats{
		Received:            p.received.Load(),
		Normalized:          p.normalized.Load(),
		Stored:              p.stored.Load(),
		Deduplicated:        p.dedup.Load(),
		Rejected:            p.rejected.Load(),
		NormalizationErrors: p.normErrors.Load(),
		IngestionErrors:     p.ingErrors.Load(),
	}
}

func (p *Pipeline) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	for _, rs := range td.ResourceSpans().All() {
		resource := rs.Resource().Attributes()
		for _, ss := range rs.ScopeSpans().All() {
			for _, span := range ss.Spans().All() {
				p.received.Add(1)
				source := normalize.DetectSource(resource, span.Attributes())
				if source == "" {
					p.rejected.Add(1)
					continue
				}
				gen, ok, err := normalize.FromSpan(source, resource, span)
				if err != nil {
					p.normErrors.Add(1)
					p.logger.Debug("normalization failed", "source", source, "error", err.Error())
					continue
				}
				if !ok {
					p.rejected.Add(1)
					continue
				}
				p.normalized.Add(1)
				inserted, err := storage.InsertGeneration(ctx, p.db, gen)
				if err != nil {
					p.ingErrors.Add(1)
					return fmt.Errorf("store generation: %w", err)
				}
				if inserted {
					p.stored.Add(1)
				} else {
					p.dedup.Add(1)
				}
			}
		}
	}
	return nil
}

// ConsumeLogs counts log records only; no source's logs become generations.
func (p *Pipeline) ConsumeLogs(_ context.Context, ld plog.Logs) error {
	n := ld.LogRecordCount()
	p.received.Add(uint64(n))
	p.rejected.Add(uint64(n))
	return nil
}

// ConsumeMetrics counts metrics only; they are aggregates and would double count.
func (p *Pipeline) ConsumeMetrics(_ context.Context, md pmetric.Metrics) error {
	var n int
	for _, rm := range md.ResourceMetrics().All() {
		for _, sm := range rm.ScopeMetrics().All() {
			n += sm.Metrics().Len()
		}
	}
	p.received.Add(uint64(n))
	p.rejected.Add(uint64(n))
	return nil
}
