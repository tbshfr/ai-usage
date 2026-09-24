package ingest

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/sanitizeotel"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func claudeCodeLogFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../../testdata/claude/logs-*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("fixtures: %v %v", paths, err)
	}
	return paths
}

func TestConsumeClaudeCodeCapturedFixtures(t *testing.T) {
	ctx := context.Background()
	pipeline := newStatsPipeline(t)
	for retry := 0; retry < 2; retry++ {
		for _, path := range claudeCodeLogFixtures(t) {
			if err := pipeline.ConsumeLogs(ctx, decodeLogs(t, path)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := pipeline.ConsumeMetrics(ctx, decodeMetrics(t, "../../testdata/claude/metrics.json")); err != nil {
		t.Fatal(err)
	}
	stats := pipeline.Stats()
	if stats.Received != 36 || stats.Normalized != 8 || stats.Stored != 4 ||
		stats.Deduplicated != 4 || stats.IgnoredNotUsed != 28 || stats.NormalizationErrors != 0 {
		t.Errorf("captured fixture stats = %+v", stats)
	}
	// Includes the Haiku session-title call, which is real spend.
	summary, err := storage.Summary(ctx, pipeline.db, storage.Filter{
		From:   time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		Source: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 4 || summary.InputTokens != 910 || summary.OutputTokens != 999 ||
		summary.CacheReadTokens != 92491 || summary.CacheCreationTokens != 22313 ||
		summary.TotalTokens() != 116713 || summary.CostKnownCount != 4 ||
		summary.CostTotal == nil || math.Abs(*summary.CostTotal-0.2177002) > 1e-9 {
		t.Errorf("captured fixture summary = %+v total=%d", summary, summary.TotalTokens())
	}
	// Main-thread Opus calls report effort; the Haiku title call does not.
	gens, err := storage.RecentGenerations(ctx, pipeline.db, storage.Filter{
		From:   time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		Source: "claude-code",
	}, storage.OrderDesc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	efforts := map[string]string{}
	for _, gen := range gens {
		efforts[gen.Model] += gen.ReasoningEffort + ","
	}
	if efforts["claude-opus-5-5"] != "medium,medium,medium," || efforts["claude-haiku-4-5-20251001"] != "," {
		t.Errorf("stored efforts = %v", efforts)
	}
}

func TestClaudeCodeCapturedFixturesAreSanitized(t *testing.T) {
	for _, path := range claudeCodeLogFixtures(t) {
		logs := decodeLogs(t, path)
		for _, rl := range logs.ResourceLogs().All() {
			for _, sl := range rl.ScopeLogs().All() {
				for _, record := range sl.LogRecords().All() {
					for _, key := range []string{"session.id", "user.email", "user.account_id",
						"user.account_uuid", "user.id", "organization.id", "prompt"} {
						if value, found := record.Attributes().Get(key); found &&
							(value.Type() != pcommon.ValueTypeStr || value.Str() != sanitizeotel.Redacted) {
							t.Errorf("%s contains unredacted %s", path, key)
						}
					}
				}
			}
		}
	}
}
