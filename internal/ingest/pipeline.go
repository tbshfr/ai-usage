package ingest

import (
	"context"
	"database/sql"
	"errors"
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

// Reason kinds and reasons of the per-reason stats breakdown. Both are a
// FIXED ENUM: values are persisted into stats_daily_reasons, so a new
// value must come with a migration-safe review. Free-form strings (model
// names, error texts) must never be used, or any authenticated client
// could grow the table without bound.
const (
	ReasonKindRejected   = "rejected"
	ReasonKindIgnored    = "ignored"
	ReasonKindNormError  = "norm_error"
	ReasonKindDedup      = "dedup"
	ReasonKindHTTPReject = "http_reject"
)

const (
	ReasonNoSource      = "no_source"        // span carries no known source's markers
	ReasonNotGeneration = "not_a_generation" // healthy span, but never a generation record
	ReasonLogs          = "logs"             // log records are ignored (see Stats.IgnoredNotUsed)
	ReasonMetrics       = "metrics"          // metric datapoints are ignored (see Stats.IgnoredNotUsed)
	ReasonBadAttrs      = "bad_attributes"   // known attribute present with a non-string value
	ReasonBadIDs        = "bad_ids"          // empty trace/span ID, no dedup key derivable
	ReasonNormOther     = "other"            // unclassified normalization error
)

// HTTP-reject reasons for the http_reject kind. When adding a value here,
// add it to HTTPRejectReasons below — BumpHTTPReject drops anything not
// allowlisted, so a missing entry silently loses counts (Warn only).
const (
	ReasonUnauthorized     = "unauthorized"         // OTLP/HTTP request without a valid bearer token
	ReasonGRPCUnauthorized = "grpc_unauthenticated" // OTLP/gRPC export without a valid bearer token
	ReasonBadContentType   = "bad_content_type"     // missing or unsupported content type
	ReasonBadEncoding      = "bad_content_encoding"
	ReasonBodyTooLarge     = "body_too_large"
	ReasonBodyReadError    = "body_read_error"
	ReasonBadGzip          = "bad_gzip"
	ReasonDecodeFailed     = "decode_failed"
)

// HTTPRejectReasons is the canonical list of http_reject reasons. The
// (day, kind, reason) rows are a fixed enum, so BumpHTTPReject must never
// accept request-derived strings (e.g. a content-type value) — that would
// let any client grow stats_daily_reasons without bound.
var HTTPRejectReasons = []string{
	ReasonUnauthorized,
	ReasonGRPCUnauthorized,
	ReasonBadContentType,
	ReasonBadEncoding,
	ReasonBodyTooLarge,
	ReasonBodyReadError,
	ReasonBadGzip,
	ReasonDecodeFailed,
}

var validHTTPRejectReasons = func() map[string]struct{} {
	m := make(map[string]struct{}, len(HTTPRejectReasons))
	for _, r := range HTTPRejectReasons {
		m[r] = struct{}{}
	}
	return m
}()

// ReasonKindOrder is the canonical display order of the breakdown groups,
// shared by the dashboard UI and the JSON API so the same data renders in
// the same order everywhere.
var ReasonKindOrder = []string{
	ReasonKindRejected,
	ReasonKindIgnored,
	ReasonKindNormError,
	ReasonKindDedup,
	ReasonKindHTTPReject,
}

// ReasonKindRank returns the display rank of a kind (lower sorts first);
// unknown kinds sort after all known kinds.
func ReasonKindRank(kind string) int {
	for i, k := range ReasonKindOrder {
		if k == kind {
			return i
		}
	}
	return len(ReasonKindOrder)
}

// ReasonCounts groups per-reason counters by kind, e.g.
// ["rejected"]["no_source"] = 3. Only non-zero entries are present.
type ReasonCounts map[string]map[string]uint64

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

	// Per-reason counters of the stats breakdown (see ReasonCounts). The
	// map only ever holds fixed-enum keys; the mutex guards map access,
	// the atomic values make concurrent Add lock-free after first use.
	reasonsMu sync.Mutex
	reasons   map[reasonKey]*atomic.Uint64

	// mu guards the persisted part of the current day's counters: base
	// holds what was already saved to stats_daily for baseDay before this
	// process started (or before the last UTC midnight rollover), so the
	// live totals in Stats() continue across restarts instead of
	// resetting to zero. Stats takes the read lock, so concurrent reads
	// are never serialized against each other, but a read can briefly
	// block behind a save's write lock (a single small upsert per save
	// interval); only Save and RestoreBase write.
	mu          sync.RWMutex
	base        Stats
	baseReasons map[reasonKey]uint64
	baseDay     string
}

// reasonKey is one fixed-enum cell of the stats breakdown.
type reasonKey struct{ kind, reason string }

// NewPipeline builds a pipeline; hub may be nil to skip notifications.
func NewPipeline(db *sql.DB, logger *slog.Logger, hub *live.Hub) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{db: db, logger: logger, hub: hub, reasons: map[reasonKey]*atomic.Uint64{}}
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

// bumpReason adds n to the fixed-enum (kind, reason) counter.
func (p *Pipeline) bumpReason(kind, reason string, n uint64) {
	if n == 0 {
		return
	}
	key := reasonKey{kind, reason}
	p.reasonsMu.Lock()
	c, ok := p.reasons[key]
	if !ok {
		c = &atomic.Uint64{}
		p.reasons[key] = c
	}
	p.reasonsMu.Unlock()
	c.Add(n)
}

// BumpHTTPReject records a transport-level rejection (auth failure,
// malformed request) that never reaches the pipeline's record counters.
// Called from the OTLP receivers and the auth middleware; safe for
// concurrent use. Unauthenticated callers can only bump these integer
// counters — the (day, kind, reason) rows are a fixed enum, so flooding
// inflates numbers, never the table. Unknown reasons are dropped (with a
// warning) instead of creating new rows, so a future caller passing
// request-derived data cannot grow the table without bound.
func (p *Pipeline) BumpHTTPReject(reason string) {
	if _, ok := validHTTPRejectReasons[reason]; !ok {
		p.logger.Warn("unknown http reject reason dropped", "reason", reason)
		return
	}
	p.bumpReason(ReasonKindHTTPReject, reason, 1)
}

// ReasonCounts returns today's effective per-reason counters: the
// persisted base for today (so counts continue across restarts) plus this
// session's counters. Zero entries are omitted.
func (p *Pipeline) ReasonCounts() ReasonCounts {
	today := utcDay(time.Now())
	combined := map[reasonKey]uint64{}
	p.mu.RLock()
	if p.baseDay == today {
		for k, v := range p.baseReasons {
			combined[k] = v
		}
	}
	p.mu.RUnlock()
	p.reasonsMu.Lock()
	for k, c := range p.reasons {
		if n := c.Load(); n > 0 {
			combined[k] += n
		}
	}
	p.reasonsMu.Unlock()
	out := ReasonCounts{}
	for k, v := range combined {
		if v == 0 {
			continue
		}
		if out[k.kind] == nil {
			out[k.kind] = map[string]uint64{}
		}
		out[k.kind][k.reason] = v
	}
	return out
}

// normErrorReason classifies a normalization error into the fixed-enum
// reason set; unclassified errors fall back to "other" instead of leaking
// error text into the stats tables.
func normErrorReason(err error) string {
	switch {
	case errors.Is(err, normalize.ErrNonStringAttrs):
		return ReasonBadAttrs
	case errors.Is(err, normalize.ErrMissingSpanIDs):
		return ReasonBadIDs
	default:
		return ReasonNormOther
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
	reasons, _, err := storage.DailyReasonsForDay(ctx, p.db, day)
	if err != nil {
		return fmt.Errorf("load daily stats reasons for %s: %w", day, err)
	}
	baseReasons := make(map[reasonKey]uint64, len(reasons))
	for _, r := range reasons {
		baseReasons[reasonKey{r.Kind, r.Reason}] = uint64(r.Count)
	}
	// Always assign the base, even when the stats row is missing: a
	// partial save (reasons committed, stats failed) leaves orphan reason
	// rows, and dropping them here would let the next Save overwrite them
	// with session-only values. A missing row yields a zero Stats base,
	// which is exactly the previous behavior for that case.
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
	p.baseReasons = baseReasons
	p.baseDay = day
	p.mu.Unlock()
	if !found {
		return nil
	}
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
// RestoreBase always records the startup day in baseDay (even when no row
// exists yet), so the rollover branch can attribute pre-midnight session
// counters to the correct day after a cold start near midnight.
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
	return storage.UpsertDailySnapshot(ctx, p.db, storage.DailyStats{
		Day:                 day,
		Received:            int64(s.Received),
		Normalized:          int64(s.Normalized),
		Stored:              int64(s.Stored),
		Deduplicated:        int64(s.Deduplicated),
		Rejected:            int64(s.Rejected),
		IgnoredNotUsed:      int64(s.IgnoredNotUsed),
		NormalizationErrors: int64(s.NormalizationErrors),
		IngestionErrors:     int64(s.IngestionErrors),
	}, p.snapshotReasonsLocked())
}

// snapshotReasonsLocked builds the day's per-reason counters (persisted
// base + this session) as an absolute snapshot. Both Save branches call it
// only for p.baseDay, which is the day baseReasons belongs to. The caller
// must hold p.mu; the combined slice is persisted together with the stats
// row in a single transaction (see UpsertDailySnapshot).
func (p *Pipeline) snapshotReasonsLocked() []storage.ReasonStat {
	combined := map[reasonKey]uint64{}
	for k, v := range p.baseReasons {
		combined[k] = v
	}
	p.reasonsMu.Lock()
	for k, c := range p.reasons {
		if n := c.Load(); n > 0 {
			combined[k] += n
		}
	}
	p.reasonsMu.Unlock()
	stats := make([]storage.ReasonStat, 0, len(combined))
	for k, v := range combined {
		if v == 0 {
			continue
		}
		stats = append(stats, storage.ReasonStat{Kind: k.kind, Reason: k.reason, Count: int64(v)})
	}
	return stats
}

// resetLocked zeroes the persisted base and the session counters after
// the old day's totals have been saved. The counters are reset one at a
// time, so a concurrent Stats() read mid-rollover can observe a torn
// snapshot (e.g. received already zeroed, stored still holding old-day
// values) for a few microseconds; the next read is consistent again.
// Accepted non-atomicity, not worth a lock-wide counter reset.
func (p *Pipeline) resetLocked() {
	p.base = Stats{}
	p.baseReasons = nil
	p.reasonsMu.Lock()
	p.reasons = map[reasonKey]*atomic.Uint64{}
	p.reasonsMu.Unlock()
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
					p.bumpReason(ReasonKindRejected, ReasonNoSource, 1)
					continue
				}
				gen, ok, err := normalize.FromSpan(source, resource, span)
				if err != nil {
					p.normErrors.Add(1)
					p.bumpReason(ReasonKindNormError, normErrorReason(err), 1)
					p.logger.Debug("normalization failed", "source", source, "error", err.Error())
					continue
				}
				if !ok {
					p.rejected.Add(1)
					p.bumpReason(ReasonKindRejected, ReasonNotGeneration, 1)
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
	storedBySource, err := storage.InsertGenerations(ctx, p.db, gens)
	if err != nil {
		p.ingErrors.Add(1)
		// The batch is atomic, so a failure means nothing was stored
		// and the dashboard has nothing new to show; exporters retry.
		return fmt.Errorf("store generations: %w", err)
	}
	var stored uint64
	for _, n := range storedBySource {
		stored += uint64(n)
	}
	p.stored.Add(stored)
	if dedup := uint64(len(gens)) - stored; dedup > 0 {
		p.dedup.Add(dedup)
		// Per-source dedup breakdown: the batch is grouped by the fixed
		// source enum, so the keys stay bounded.
		for src, n := range countBySource(gens) {
			if d := uint64(n) - uint64(storedBySource[src]); d > 0 {
				p.bumpReason(ReasonKindDedup, src, d)
			}
		}
	}
	// One notification per stored batch: bursts of spans coalesce into a
	// single "data changed" signal for the dashboard's SSE stream.
	if p.hub != nil && stored > 0 {
		p.hub.Notify()
	}
	return nil
}

// countBySource counts generations per source (fixed enum).
func countBySource(gens []normalize.Generation) map[string]int {
	out := make(map[string]int)
	for _, g := range gens {
		out[g.Source]++
	}
	return out
}

// ConsumeLogs counts log records only; no source's logs become generations,
// so they are ignored (see Stats.IgnoredNotUsed).
func (p *Pipeline) ConsumeLogs(_ context.Context, ld plog.Logs) error {
	n := ld.LogRecordCount()
	p.received.Add(uint64(n))
	p.ignored.Add(uint64(n))
	p.bumpReason(ReasonKindIgnored, ReasonLogs, uint64(n))
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
	p.bumpReason(ReasonKindIgnored, ReasonMetrics, uint64(n))
	return nil
}
