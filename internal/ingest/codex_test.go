package ingest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestConsumeCodexLogsStoresDeduplicatesAndIgnores(t *testing.T) {
	pipeline := newStatsPipeline(t)
	ctx := context.Background()
	responses := decodeLogs(t, "../../testdata/codex/logs-sse-events.json")

	if err := pipeline.ConsumeLogs(ctx, responses); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ConsumeLogs(ctx, responses); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ConsumeLogs(ctx, decodeLogs(t, "../../testdata/codex/logs-other.json")); err != nil {
		t.Fatal(err)
	}

	malformed := plog.NewLogs()
	rl := malformed.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "codex_cli_rs")
	record := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Attributes().PutStr("event.name", "codex.sse_event")
	record.Attributes().PutStr("event.kind", "response.completed")
	record.Attributes().PutStr("event.timestamp", "2026-09-07T07:36:00Z")
	record.Attributes().PutStr("conversation.id", "test")
	record.Attributes().PutInt("model", 42)
	if err := pipeline.ConsumeLogs(ctx, malformed); err != nil {
		t.Fatal(err)
	}

	stats := pipeline.Stats()
	if stats.Received != 7 || stats.Normalized != 4 || stats.Stored != 2 || stats.Deduplicated != 2 ||
		stats.IgnoredNotUsed != 2 || stats.NormalizationErrors != 1 || stats.Rejected != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if got := pipeline.ReasonCounts()[ReasonKindDedup]["codex"]; got != 2 {
		t.Errorf("codex dedup reason = %d, want 2", got)
	}

	summary, err := storage.Summary(ctx, pipeline.db, storage.Filter{
		From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.InputTokens != 11297 || summary.OutputTokens != 113 ||
		summary.CacheReadTokens != 23296 || summary.CacheCreationTokens != 0 || summary.ReasoningTokens != 19 ||
		summary.TotalTokens() != 34725 || summary.CostKnownCount != 0 || summary.CostUnknownCount != 2 {
		t.Errorf("summary = %+v total=%d", summary, summary.TotalTokens())
	}
	rate := summary.CacheHitRate()
	wantRate := float64(23296) / float64(11297+23296)
	if rate == nil || math.Abs(*rate-wantRate) > 1e-12 {
		t.Errorf("cache hit rate = %v, want %v", rate, wantRate)
	}
}

func TestCodexTraceAndMetricsRemainNonAuthoritative(t *testing.T) {
	pipeline := newStatsPipeline(t)
	ctx := context.Background()
	traces := decodeTraces(t, "../../testdata/codex/traces-codex.json")
	metrics := decodeMetrics(t, "../../testdata/codex/metrics-codex.json")
	if err := pipeline.ConsumeTraces(ctx, traces); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ConsumeMetrics(ctx, metrics); err != nil {
		t.Fatal(err)
	}
	stats := pipeline.Stats()
	if stats.Received != 3 || stats.Rejected != 2 || stats.IgnoredNotUsed != 1 || stats.Stored != 0 {
		t.Errorf("stats = %+v", stats)
	}
}
