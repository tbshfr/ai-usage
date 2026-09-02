package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const batchTimeout = 10 * time.Second

type Stats struct {
	Received     uint64
	Normalized   uint64
	Stored       uint64
	Deduplicated uint64
	Rejected     uint64
	// IgnoredNotUsed counts records that arrived and were healthy but can
	// never become generations: log records (no source exports generations
	// via logs) and metric datapoints (aggregates that would double count
	// tokens already captured by spans). Not an error and not a rejection.
	IgnoredNotUsed      uint64
	NormalizationErrors uint64
	IngestionErrors     uint64
}

// Pipeline routes decoded signals into normalized Generation records.
// Per-record failures never fail the batch; DB failures do (so exporters
// retry — dedup makes retries safe). When a hub is set, each batch that
// stored at least one new generation notifies it once, so the dashboard
// can refresh its live fragments.
type Pipeline struct {
	db     *sql.DB
	logger *slog.Logger
	hub    *live.Hub

	received   atomic.Uint64
	normalized atomic.Uint64
	stored     atomic.Uint64
	dedup      atomic.Uint64
	rejected   atomic.Uint64
	ignored    atomic.Uint64
	normErrors atomic.Uint64
	ingErrors  atomic.Uint64

	// mu guards the persisted part of the current day's counters: base
	// holds what was already saved to stats_daily for baseDay before this
	// process started (or before the last UTC midnight rollover), so the
	// live totals in Stats() continue across restarts instead of
	// resetting to zero. Stats takes the read lock, so concurrent reads
	// are never serialized against each other, but a read can briefly
	// block behind a save's write lock (a single small upsert per save
	// interval); only Save and RestoreBase write.
	mu      sync.RWMutex
	base    Stats
	baseDay string
}

// NewPipeline builds a pipeline; hub may be nil to skip notifications.
func NewPipeline(db *sql.DB, logger *slog.Logger, hub *live.Hub) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{db: db, logger: logger, hub: hub}
}

func (p *Pipeline) Stats() Stats {
	// The persisted base only counts toward today's totals on the day it
	// was recorded for; after a midnight rollover (before the next save)
	// today's row is empty, so the effective base is zero.
	var base Stats
	today := utcDay(time.Now())
	p.mu.RLock()
	if p.baseDay == today {
		base = p.base
	}
	p.mu.RUnlock()
	return base.add(p.atomicStats())
}

func (p *Pipeline) atomicStats() Stats {
	return Stats{
		Received:            p.received.Load(),
		Normalized:          p.normalized.Load(),
		Stored:              p.stored.Load(),
		Deduplicated:        p.dedup.Load(),
		Rejected:            p.rejected.Load(),
		IgnoredNotUsed:      p.ignored.Load(),
		NormalizationErrors: p.normErrors.Load(),
		IngestionErrors:     p.ingErrors.Load(),
	}
}

func (s Stats) add(o Stats) Stats {
	return Stats{
		Received:            s.Received + o.Received,
		Normalized:          s.Normalized + o.Normalized,
		Stored:              s.Stored + o.Stored,
		Deduplicated:        s.Deduplicated + o.Deduplicated,
		Rejected:            s.Rejected + o.Rejected,
		IgnoredNotUsed:      s.IgnoredNotUsed + o.IgnoredNotUsed,
		NormalizationErrors: s.NormalizationErrors + o.NormalizationErrors,
		IngestionErrors:     s.IngestionErrors + o.IngestionErrors,
	}
}

func utcDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// RestoreBase loads today's persisted counters as the base the live
// session counters add up to. Call once at startup, after migrations.
// A missing row for today is not an error (the base stays zero), but a
// failed read is: callers should abort rather than run with a zero base,
// or the next Save would overwrite today's persisted counters with
// session-only values.
func (p *Pipeline) RestoreBase(ctx context.Context) error {
	day := utcDay(time.Now())
	row, found, err := storage.DailyStatsForDay(ctx, p.db, day)
	if err != nil {
		return fmt.Errorf("load daily stats for %s: %w", day, err)
	}
	if !found {
		return nil
	}
	p.mu.Lock()
	p.base = Stats{
		Received:            uint64(row.Received),
		Normalized:          uint64(row.Normalized),
		Stored:              uint64(row.Stored),
		Deduplicated:        uint64(row.Deduplicated),
		Rejected:            uint64(row.Rejected),
		IgnoredNotUsed:      uint64(row.IgnoredNotUsed),
		NormalizationErrors: uint64(row.NormalizationErrors),
		IngestionErrors:     uint64(row.IngestionErrors),
	}
	p.baseDay = day
	p.mu.Unlock()
	p.logger.Info("stats base restored", "day", day, "received", row.Received)
	return nil
}

// StartSaver persists the day's counters every interval until ctx is
// cancelled. It also handles the UTC midnight rollover: a timer aligned
// to the next midnight triggers a save at the day boundary, so the final
// old-day totals are written and the session counters restart from zero
// right away instead of waiting for the next periodic tick.
func (p *Pipeline) StartSaver(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		midnight := time.NewTimer(time.Until(nextUTCMidnight(time.Now())))
		defer midnight.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.Save(); err != nil {
					p.logger.Warn("stats save failed", "error", err.Error())
				}
			case <-midnight.C:
				midnight.Reset(time.Until(nextUTCMidnight(time.Now())))
				if err := p.Save(); err != nil {
					p.logger.Warn("stats save failed", "error", err.Error())
				}
			}
		}
	}()
}

// nextUTCMidnight returns the start of the UTC day after t. UTC has no
// DST transitions, so adding 24h to the truncated day is always exact.
func nextUTCMidnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

// Save persists the day's counters (persisted base + this session) as an
// absolute snapshot. Called periodically by StartSaver and once on
// shutdown. When the UTC day rolled over since the last save, everything
// so far is written to the old day and the session counters restart from
// zero for the new day (records consumed in the instant between snapshot
// and reset are the only casualty, a sub-millisecond window).
//
// Known edge: a cold start (baseDay == "" from a missing row for today)
// within statsSaveInterval of UTC midnight writes the pre-midnight session
// counters to the new day on the first periodic save, because the rollover
// branch needs a baseDay to file the old totals under. The window is tiny
// and only shifts one day's attribution slightly; accepted trade-off.
func (p *Pipeline) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	today := utcDay(time.Now())
	snap := p.base.add(p.atomicStats())
	if p.baseDay != "" && p.baseDay != today {
		if err := p.persistLocked(p.baseDay, snap); err != nil {
			return err
		}
		p.resetLocked()
		p.baseDay = today
		return nil
	}
	p.baseDay = today
	return p.persistLocked(today, snap)
}

func (p *Pipeline) persistLocked(day string, s Stats) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return storage.UpsertDailyStats(ctx, p.db, storage.DailyStats{
		Day:                 day,
		Received:            int64(s.Received),
		Normalized:          int64(s.Normalized),
		Stored:              int64(s.Stored),
		Deduplicated:        int64(s.Deduplicated),
		Rejected:            int64(s.Rejected),
		IgnoredNotUsed:      int64(s.IgnoredNotUsed),
		NormalizationErrors: int64(s.NormalizationErrors),
		IngestionErrors:     int64(s.IngestionErrors),
	})
}

// resetLocked zeroes the persisted base and the session counters after
// the old day's totals have been saved. The counters are reset one at a
// time, so a concurrent Stats() read mid-rollover can observe a torn
// snapshot (e.g. received already zeroed, stored still holding old-day
// values) for a few microseconds; the next read is consistent again.
// Accepted non-atomicity, not worth a lock-wide counter reset.
func (p *Pipeline) resetLocked() {
	p.base = Stats{}
	p.received.Store(0)
	p.normalized.Store(0)
	p.stored.Store(0)
	p.dedup.Store(0)
	p.rejected.Store(0)
	p.ignored.Store(0)
	p.normErrors.Store(0)
	p.ingErrors.Store(0)
}

func (p *Pipeline) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	var gens []normalize.Generation
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
				gens = append(gens, gen)
			}
		}
	}
	// One transaction per batch: a single commit instead of one per span
	// keeps the write lock held once and briefly, so dashboard reads never
	// queue behind a long series of writes.
	inserted, err := storage.InsertGenerations(ctx, p.db, gens)
	if err != nil {
		p.ingErrors.Add(1)
		// The batch is atomic, so a failure means nothing was stored
		// and the dashboard has nothing new to show; exporters retry.
		return fmt.Errorf("store generations: %w", err)
	}
	p.stored.Add(uint64(inserted))
	p.dedup.Add(uint64(len(gens) - inserted))
	// One notification per stored batch: bursts of spans coalesce into a
	// single "data changed" signal for the dashboard's SSE stream.
	if p.hub != nil && inserted > 0 {
		p.hub.Notify()
	}
	return nil
}

// ConsumeLogs counts log records only; no source's logs become generations,
// so they are ignored (see Stats.IgnoredNotUsed).
func (p *Pipeline) ConsumeLogs(_ context.Context, ld plog.Logs) error {
	n := ld.LogRecordCount()
	p.received.Add(uint64(n))
	p.ignored.Add(uint64(n))
	return nil
}

// ConsumeMetrics counts metrics only; they are aggregates and would double
// count, so they are ignored (see Stats.IgnoredNotUsed).
func (p *Pipeline) ConsumeMetrics(_ context.Context, md pmetric.Metrics) error {
	var n int
	for _, rm := range md.ResourceMetrics().All() {
		for _, sm := range rm.ScopeMetrics().All() {
			n += sm.Metrics().Len()
		}
	}
	p.received.Add(uint64(n))
	p.ignored.Add(uint64(n))
	return nil
}
