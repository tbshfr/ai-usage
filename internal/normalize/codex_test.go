package normalize

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestFromCodexLogFixture(t *testing.T) {
	records := loadLogs(t, "../../testdata/codex/logs-sse-events.json")
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	var withReasoning Generation
	for _, item := range records {
		if got := DetectLogSource(item.resource); got != SourceCodex {
			t.Fatalf("DetectLogSource = %q", got)
		}
		gen, ok, err := FromCodexLog(item.resource, item.record)
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if gen.Source != SourceCodex || gen.ServiceName != "codex_cli_rs" || gen.Model != "gpt-5.6-luna" {
			t.Errorf("identity = source %q service %q model %q", gen.Source, gen.ServiceName, gen.Model)
		}
		if gen.Provider != "" || gen.Cost != nil || gen.TraceID != "" || gen.SpanID != "" || gen.Duration != 0 {
			t.Errorf("unsupported metadata was fabricated: %+v", gen)
		}
		if gen.ReasoningTokens != nil && *gen.ReasoningTokens > 0 {
			withReasoning = gen
		}
	}
	if withReasoning.ID == "" {
		t.Fatal("fixture has no record with reasoning tokens")
	}
	assertToken(t, "raw input", withReasoning.InputTokens, 24276)
	assertToken(t, "raw output", withReasoning.OutputTokens, 132)
	assertToken(t, "cache read", withReasoning.CacheReadTokens, 23296)
	assertToken(t, "cache create", withReasoning.CacheCreationTokens, 0)
	assertToken(t, "reasoning", withReasoning.ReasoningTokens, 19)
	assertToken(t, "uncached input", withReasoning.UncachedInput(), 980)
	assertToken(t, "non-reasoning output", withReasoning.NonReasoningOutput(), 113)
	if got := *withReasoning.UncachedInput() + *withReasoning.NonReasoningOutput() +
		*withReasoning.CacheReadTokens + *withReasoning.CacheCreationTokens + *withReasoning.ReasoningTokens; got != 24408 {
		t.Errorf("canonical total = %d, want 24408", got)
	}
}

func TestFromCodexLogIgnoresNonTerminalEvents(t *testing.T) {
	for _, item := range loadLogs(t, "../../testdata/codex/logs-other.json") {
		if _, ok, err := FromCodexLog(item.resource, item.record); err != nil || ok {
			t.Errorf("non-terminal event: ok=%v err=%v", ok, err)
		}
	}
}

func TestFromCodexLogValidationAndTimestampFallback(t *testing.T) {
	resource := pcommon.NewMap()
	resource.PutStr("service.name", "codex_cli_rs")

	malformed := codexRecord(map[string]any{
		"event.name":      "codex.sse_event",
		"event.kind":      "response.completed",
		"event.timestamp": "2026-09-07T07:35:57.402Z",
		"conversation.id": "conversation",
		"model":           "model",
		"input_token_count": map[string]any{
			"nested": "not numeric",
		},
	})
	if _, ok, err := FromCodexLog(resource, malformed); !ok || !errors.Is(err, ErrInvalidTokenAttr) {
		t.Fatalf("malformed numeric: ok=%v err=%v", ok, err)
	}

	fallback := codexRecord(map[string]any{
		"event.name":      "codex.sse_event",
		"event.kind":      "response.completed",
		"conversation.id": "conversation",
		"model":           "model",
	})
	fallback.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Unix(123, 456)))
	gen, ok, err := FromCodexLog(resource, fallback)
	if err != nil || !ok || !gen.Timestamp.Equal(time.Unix(123, 456)) {
		t.Fatalf("fallback timestamp = %v, ok=%v err=%v", gen.Timestamp, ok, err)
	}

	fallback.SetObservedTimestamp(0)
	if _, ok, err := FromCodexLog(resource, fallback); !ok || !errors.Is(err, ErrInvalidLogTimestamp) {
		t.Fatalf("missing timestamp: ok=%v err=%v", ok, err)
	}
}

func TestDedupLogIDCanonicalAndSensitive(t *testing.T) {
	ts := time.Date(2026, 9, 7, 7, 35, 57, 402123456, time.FixedZone("offset", 2*60*60))
	zero := int64(0)
	first, err := DedupLogID(SourceCodex, "conversation", ts, "model", &zero, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := DedupLogID(SourceCodex, "conversation", ts.UTC(), "model", &zero, nil)
	if first != same {
		t.Error("equivalent instants must deduplicate")
	}
	different, _ := DedupLogID(SourceCodex, "conversation", ts.Add(time.Nanosecond), "model", &zero, nil)
	if first == different {
		t.Error("nanosecond timestamp change must change ID")
	}
	nilVsZero, _ := DedupLogID(SourceCodex, "conversation", ts, "model", &zero, &zero)
	if first == nilVsZero {
		t.Error("nil and explicit zero token values must not collide")
	}
}

func TestFromCodexLogDedupCanonicalizesNumericEncoding(t *testing.T) {
	resource := pcommon.NewMap()
	resource.PutStr("service.name", "codex_cli_rs")
	base := map[string]any{
		"event.name":      "codex.sse_event",
		"event.kind":      "response.completed",
		"event.timestamp": "2026-09-07T07:35:57.402123456Z",
		"conversation.id": "conversation",
		"model":           "model",
	}
	stringsRecord := codexRecord(base)
	intsRecord := codexRecord(base)
	for _, key := range []string{"input_token_count", "output_token_count", "cached_token_count", "cache_write_token_count", "reasoning_token_count"} {
		stringsRecord.Attributes().PutStr(key, "7")
		intsRecord.Attributes().PutInt(key, 7)
	}
	stringsGen, ok, err := FromCodexLog(resource, stringsRecord)
	if err != nil || !ok {
		t.Fatalf("string encoding: ok=%v err=%v", ok, err)
	}
	intsGen, ok, err := FromCodexLog(resource, intsRecord)
	if err != nil || !ok {
		t.Fatalf("int encoding: ok=%v err=%v", ok, err)
	}
	if stringsGen.ID != intsGen.ID {
		t.Error("equivalent string/int token encodings must deduplicate")
	}

	missing := codexRecord(base)
	missingGen, ok, err := FromCodexLog(resource, missing)
	if err != nil || !ok {
		t.Fatalf("missing tokens: ok=%v err=%v", ok, err)
	}
	if missingGen.InputTokens != nil || missingGen.OutputTokens != nil || missingGen.CacheReadTokens != nil ||
		missingGen.CacheCreationTokens != nil || missingGen.ReasoningTokens != nil {
		t.Errorf("missing token values were fabricated: %+v", missingGen)
	}
}

func codexRecord(attrs map[string]any) plog.LogRecord {
	logs := plog.NewLogs()
	record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := record.Attributes().FromRaw(attrs); err != nil {
		panic(err)
	}
	return record
}

func assertToken(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %d", name, got, want)
	}
}
