package normalize

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func claudeCodeRecord(attrs map[string]any) (pcommon.Map, plog.LogRecord) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-code")
	record := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 9, 24, 18, 2, 20, 910000000, time.UTC)))
	record.Body().SetStr("claude_code.api_request")
	if err := record.Attributes().FromRaw(attrs); err != nil {
		panic(err)
	}
	return rl.Resource().Attributes(), record
}

func TestFromClaudeCodeLog(t *testing.T) {
	// Values mirror the captured Opus call. Claude Code exports numbers as
	// strings, and input excludes cache reads and writes.
	resource, record := claudeCodeRecord(map[string]any{
		"event.name": "api_request", "event.sequence": "11",
		"session.id": "session-1", "prompt.id": "prompt-1",
		"request_id": "req_1", "model": "claude-opus-5-5",
		"input_tokens": "2", "output_tokens": "525",
		"cache_read_tokens": "26473", "cache_creation_tokens": "13072",
		"cost_usd": 0.1203786, "duration_ms": "5924",
		"query_source": "repl_main_thread",
	})
	if got := DetectLogSource(resource); got != SourceClaudeCode {
		t.Fatalf("source = %q", got)
	}
	gen, ok, err := FromLog(SourceClaudeCode, resource, record)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.Source != SourceClaudeCode || gen.ServiceName != "claude-code" || gen.ConversationID != "session-1" ||
		gen.Model != "claude-opus-5-5" || gen.Provider != "anthropic" || gen.Duration != 5924*time.Millisecond {
		t.Errorf("identity/duration = %+v", gen)
	}
	assertToken(t, "input", gen.UncachedInput(), 2)
	assertToken(t, "output", gen.OutputTokens, 525)
	assertToken(t, "cache read", gen.CacheReadTokens, 26473)
	assertToken(t, "cache creation", gen.CacheCreationTokens, 13072)
	if gen.ReasoningTokens != nil || gen.Cost == nil || *gen.Cost != 0.1203786 ||
		!gen.CostReportedByHarness || gen.CostSource != "harness" {
		t.Errorf("cost/reasoning = %+v", gen)
	}

	// The request ID alone identifies the call, so a replay with another
	// sequence or timestamp keeps its row.
	record.Attributes().PutStr("event.sequence", "12")
	record.SetTimestamp(pcommon.NewTimestampFromTime(record.Timestamp().AsTime().Add(time.Second)))
	replay, _, _ := FromClaudeCodeLog(resource, record)
	if replay.ID != gen.ID {
		t.Error("replay changed identity")
	}
	record.Attributes().PutStr("request_id", "req_2")
	next, _, _ := FromClaudeCodeLog(resource, record)
	if next.ID == gen.ID {
		t.Error("different request_id collapsed into same row")
	}
}

func TestFromClaudeCodeLogWithoutRequestID(t *testing.T) {
	resource, record := claudeCodeRecord(map[string]any{
		"event.name": "api_request", "event.sequence": "5",
		"session.id": "session-1", "model": "claude-opus-5-5",
	})
	gen, ok, err := FromClaudeCodeLog(resource, record)
	if err != nil || !ok || gen.ID == "" {
		t.Fatalf("ok=%v err=%v id=%q", ok, err, gen.ID)
	}
	record.Attributes().PutStr("event.sequence", "6")
	next, _, _ := FromClaudeCodeLog(resource, record)
	if next.ID == gen.ID {
		t.Error("different event.sequence collapsed into same row")
	}
	record.Attributes().Remove("session.id")
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrMissingLogIdentity) {
		t.Fatalf("no request or session ID: ok=%v err=%v", ok, err)
	}
}

func TestFromClaudeCodeLogZeroCostAllowsPricingFallback(t *testing.T) {
	resource, record := claudeCodeRecord(map[string]any{
		"event.name": "api_request", "request_id": "req_1",
		"model": "gateway-model", "cost_usd": 0.0,
	})
	gen, ok, err := FromClaudeCodeLog(resource, record)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.Cost != nil || gen.CostReportedByHarness || gen.CostSource != "" || gen.ConversationID != "" {
		t.Errorf("zero estimate should leave cost unknown: %+v", gen)
	}
}

func TestFromClaudeCodeLogIgnoresOtherEventsAndValidates(t *testing.T) {
	resource, record := claudeCodeRecord(map[string]any{
		"event.name": "assistant_response", "request_id": "req_1", "model": "claude-opus-5-5",
	})
	if _, ok, err := FromClaudeCodeLog(resource, record); ok || err != nil {
		t.Fatalf("response event: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutStr("event.name", "api_request")
	record.Attributes().PutStr("cost_usd", "bad")
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrInvalidClaudeCodeAttr) {
		t.Fatalf("invalid cost: ok=%v err=%v", ok, err)
	}
	record.Attributes().Remove("cost_usd")
	record.Attributes().PutStr("input_tokens", "-1")
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrInvalidTokenAttr) {
		t.Fatalf("invalid tokens: ok=%v err=%v", ok, err)
	}
	record.Attributes().Remove("input_tokens")
	record.Attributes().PutInt("request_id", 1)
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrNonStringAttrs) {
		t.Fatalf("non-string request_id: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutStr("request_id", "req_1")
	record.Attributes().Remove("model")
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrMissingLogIdentity) {
		t.Fatalf("missing model: ok=%v err=%v", ok, err)
	}
	record.Attributes().PutStr("model", "claude-opus-5-5")
	record.SetTimestamp(0)
	if _, ok, err := FromClaudeCodeLog(resource, record); !ok || !errors.Is(err, ErrInvalidLogTimestamp) {
		t.Fatalf("missing timestamp: ok=%v err=%v", ok, err)
	}
}
