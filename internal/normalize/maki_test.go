package normalize

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func makiRecord(attrs map[string]any) (pcommon.Map, plog.LogRecord) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "custom-maki")
	rl.Resource().Attributes().PutStr("telemetry.sdk.name", "maki-otel")
	record := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 9, 23, 12, 9, 3, 327919757, time.UTC)))
	if err := record.Attributes().FromRaw(attrs); err != nil {
		panic(err)
	}
	return rl.Resource().Attributes(), record
}

func TestFromMakiLog(t *testing.T) {
	// Values mirror the captured cache-heavy call. Input excludes cache reads.
	resource, record := makiRecord(map[string]any{
		"event.name": "maki.api_request", "event.sequence": int64(22),
		"session.id": "session-1", "model": "glm-5.3-flash", "provider": "opencode-go",
		"input_tokens": int64(826), "output_tokens": int64(495),
		"cache_read_tokens": int64(15360), "cache_creation_tokens": int64(0),
		"cost_usd": 0.0008322, "duration_ms": int64(3512),
	})
	if got := DetectLogSource(resource); got != SourceMaki {
		t.Fatalf("source = %q", got)
	}
	gen, ok, err := FromLog(SourceMaki, resource, record)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.Source != SourceMaki || gen.ServiceName != "custom-maki" || gen.ConversationID != "session-1" ||
		gen.Model != "glm-5.3-flash" || gen.Provider != "opencode-go" || gen.Duration != 3512*time.Millisecond {
		t.Errorf("identity/duration = %+v", gen)
	}
	assertToken(t, "input", gen.UncachedInput(), 826)
	assertToken(t, "output", gen.OutputTokens, 495)
	assertToken(t, "cache read", gen.CacheReadTokens, 15360)
	assertToken(t, "cache creation", gen.CacheCreationTokens, 0)
	if gen.ReasoningTokens != nil || gen.Cost == nil || *gen.Cost != 0.0008322 ||
		!gen.CostReportedByHarness || gen.CostSource != "harness" {
		t.Errorf("cost/reasoning = %+v", gen)
	}
	if gen.ID == "" {
		t.Fatal("missing dedup ID")
	}

	// An exact replay keeps its ID, while another event in the same session
	// gets a separate row even if every other field and timestamp matches.
	replay, _, _ := FromMakiLog(resource, record)
	if replay.ID != gen.ID {
		t.Error("replay changed identity")
	}
	record.Attributes().PutInt("event.sequence", 23)
	next, _, _ := FromMakiLog(resource, record)
	if next.ID == gen.ID {
		t.Error("different event.sequence collapsed into same row")
	}
	record.Attributes().PutInt("event.sequence", 22)
	record.SetTimestamp(pcommon.NewTimestampFromTime(record.Timestamp().AsTime().Add(time.Nanosecond)))
	resumed, _, _ := FromMakiLog(resource, record)
	if resumed.ID == gen.ID {
		t.Error("same sequence at a later timestamp collapsed into same row")
	}
}

func TestFromMakiLogZeroCostAllowsPricingFallback(t *testing.T) {
	resource, record := makiRecord(map[string]any{
		"event.name": "maki.api_request", "event.sequence": int64(1),
		"session.id": "session-1", "model": "unpriced-model", "cost_usd": 0.0,
	})
	gen, ok, err := FromMakiLog(resource, record)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.Cost != nil || gen.CostReportedByHarness || gen.CostSource != "" {
		t.Errorf("zero estimate should leave cost unknown: %+v", gen)
	}
}

func TestFromMakiLogIgnoresOtherEventsAndValidatesIdentity(t *testing.T) {
	resource, record := makiRecord(map[string]any{
		"event.name": "maki.tool_result", "session.id": "session-1",
	})
	if _, ok, err := FromMakiLog(resource, record); ok || err != nil {
		t.Fatalf("tool event: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutStr("event.name", "maki.api_request")
	record.Attributes().PutStr("model", "model")
	if _, ok, err := FromMakiLog(resource, record); !ok || !errors.Is(err, ErrMissingLogIdentity) {
		t.Fatalf("missing sequence: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutStr("event.sequence", "bad")
	if _, ok, err := FromMakiLog(resource, record); !ok || !errors.Is(err, ErrInvalidMakiAttr) {
		t.Fatalf("invalid sequence: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutInt("event.sequence", 1_000_000_001)
	if _, ok, err := FromMakiLog(resource, record); !ok || err != nil {
		t.Fatalf("large sequence: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutInt("event.sequence", 0)
	record.Attributes().PutInt("input_tokens", -1)
	if _, ok, err := FromMakiLog(resource, record); !ok || !errors.Is(err, ErrInvalidTokenAttr) {
		t.Fatalf("invalid tokens: ok=%v err=%v", ok, err)
	}
	record.Attributes().Remove("input_tokens")
	record.SetTimestamp(0)
	if _, ok, err := FromMakiLog(resource, record); !ok || !errors.Is(err, ErrInvalidLogTimestamp) {
		t.Fatalf("missing timestamp: ok=%v err=%v", ok, err)
	}
}
