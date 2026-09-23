package ingest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/sanitizeotel"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestConsumeMakiLogsUsesCallsNotMetrics(t *testing.T) {
	ctx := context.Background()
	pipeline := newStatsPipeline(t)
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "maki")
	sl := rl.ScopeLogs().AppendEmpty()
	stamp := pcommon.NewTimestampFromTime(time.Date(2026, 9, 23, 12, 9, 3, 327919757, time.UTC))
	for sequence := int64(0); sequence < 2; sequence++ {
		record := sl.LogRecords().AppendEmpty()
		record.SetTimestamp(stamp)
		attrs := record.Attributes()
		attrs.PutStr("event.name", "maki.api_request")
		attrs.PutStr("session.id", "session-1")
		attrs.PutInt("event.sequence", sequence)
		attrs.PutStr("model", "glm-5.3-flash")
		attrs.PutStr("provider", "opencode-go")
		attrs.PutInt("input_tokens", 826)
		attrs.PutInt("output_tokens", 495)
		attrs.PutInt("cache_read_tokens", 15360)
		attrs.PutInt("cache_creation_tokens", 0)
		attrs.PutDouble("cost_usd", 0.0008322)
	}
	sl.LogRecords().AppendEmpty().Attributes().PutStr("event.name", "maki.tool_result")
	if err := pipeline.ConsumeLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ConsumeLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("maki.token.usage")
	metric.SetEmptySum().DataPoints().AppendEmpty().SetIntValue(999999)
	if err := pipeline.ConsumeMetrics(ctx, metrics); err != nil {
		t.Fatal(err)
	}
	stats := pipeline.Stats()
	if stats.Stored != 2 || stats.Deduplicated != 2 || stats.IgnoredNotUsed != 3 || stats.NormalizationErrors != 0 {
		t.Errorf("pipeline stats = %+v", stats)
	}
	if got := pipeline.ReasonCounts()[ReasonKindDedup]["maki"]; got != 2 {
		t.Errorf("Maki dedup reason = %d, want 2", got)
	}
	summary, err := storage.Summary(ctx, pipeline.db, storage.Filter{
		From:   time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		Source: "maki",
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.InputTokens != 1652 || summary.OutputTokens != 990 ||
		summary.CacheReadTokens != 30720 || summary.TotalTokens() != 33362 ||
		summary.CostKnownCount != 2 || summary.CostTotal == nil || math.Abs(*summary.CostTotal-0.0016644) > 1e-12 {
		t.Errorf("summary = %+v total=%d", summary, summary.TotalTokens())
	}
}

func TestConsumeMakiCapturedFixtures(t *testing.T) {
	ctx := context.Background()
	pipeline := newStatsPipeline(t)
	logs := decodeLogs(t, "../../testdata/maki/logs.json")
	metrics := decodeMetrics(t, "../../testdata/maki/metrics.json")
	for retry := 0; retry < 2; retry++ {
		if err := pipeline.ConsumeLogs(ctx, logs); err != nil {
			t.Fatal(err)
		}
	}
	if err := pipeline.ConsumeMetrics(ctx, metrics); err != nil {
		t.Fatal(err)
	}
	stats := pipeline.Stats()
	if stats.Received != 24 || stats.Normalized != 6 || stats.Stored != 3 ||
		stats.Deduplicated != 3 || stats.IgnoredNotUsed != 18 || stats.NormalizationErrors != 0 {
		t.Errorf("captured fixture stats = %+v", stats)
	}
	summary, err := storage.Summary(ctx, pipeline.db, storage.Filter{
		From:   time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		Source: "maki",
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 3 || summary.InputTokens != 14007 || summary.OutputTokens != 714 ||
		summary.CacheReadTokens != 15360 || summary.CacheCreationTokens != 0 ||
		summary.TotalTokens() != 30081 || summary.CostKnownCount != 3 ||
		summary.CostTotal == nil || math.Abs(*summary.CostTotal-0.00291885) > 1e-12 {
		t.Errorf("captured fixture summary = %+v total=%d", summary, summary.TotalTokens())
	}
}

func TestMakiCapturedFixturesAreSanitized(t *testing.T) {
	logs := decodeLogs(t, "../../testdata/maki/logs.json")
	for _, rl := range logs.ResourceLogs().All() {
		for _, sl := range rl.ScopeLogs().All() {
			for _, record := range sl.LogRecords().All() {
				if record.Body().Type() != pcommon.ValueTypeEmpty {
					t.Error("Maki fixture contains a log body")
				}
				for _, key := range []string{"prompt", "tool_input", "error"} {
					if value, found := record.Attributes().Get(key); found &&
						(value.Type() != pcommon.ValueTypeStr || value.Str() != sanitizeotel.Redacted) {
						t.Errorf("Maki fixture contains unredacted %s", key)
					}
				}
				v, found := record.Attributes().Get("session.id")
				if !found || v.Type() != pcommon.ValueTypeStr || v.Str() != sanitizeotel.Redacted {
					t.Error("Maki log session.id is not redacted")
				}
			}
		}
	}
	metrics := decodeMetrics(t, "../../testdata/maki/metrics.json")
	for _, rm := range metrics.ResourceMetrics().All() {
		for _, sm := range rm.ScopeMetrics().All() {
			for _, metric := range sm.Metrics().All() {
				for _, point := range metric.Sum().DataPoints().All() {
					v, found := point.Attributes().Get("session.id")
					if !found || v.Type() != pcommon.ValueTypeStr || v.Str() != sanitizeotel.Redacted {
						t.Error("Maki metric session.id is not redacted")
					}
				}
			}
		}
	}
}
