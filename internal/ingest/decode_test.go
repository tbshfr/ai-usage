package ingest

import (
	"os"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func decodeTraces(t *testing.T, path string) ptrace.Traces {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := &ptrace.JSONUnmarshaler{}
	td, err := u.UnmarshalTraces(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return td
}

func decodeMetrics(t *testing.T, path string) pmetric.Metrics {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := &pmetric.JSONUnmarshaler{}
	md, err := u.UnmarshalMetrics(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return md
}

func decodeLogs(t *testing.T, path string) plog.Logs {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := &plog.JSONUnmarshaler{}
	ld, err := u.UnmarshalLogs(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return ld
}

func allSpans(t *testing.T, td ptrace.Traces) []ptrace.Span {
	t.Helper()
	var spans []ptrace.Span
	for _, rs := range td.ResourceSpans().All() {
		for _, ss := range rs.ScopeSpans().All() {
			for _, span := range ss.Spans().All() {
				spans = append(spans, span)
			}
		}
	}
	return spans
}

func opName(span ptrace.Span) string {
	if v, ok := span.Attributes().Get("gen_ai.operation.name"); ok {
		return v.Str()
	}
	return ""
}

func attrInt(t *testing.T, m pcommon.Map, key string) (int64, bool) {
	t.Helper()
	v, ok := m.Get(key)
	if !ok {
		return 0, false
	}
	if v.Type() != pcommon.ValueTypeInt {
		t.Fatalf("attribute %s: want int, got %s", key, v.Type())
	}
	return v.Int(), true
}

func TestDecodeCopilotFixtures(t *testing.T) {
	t.Run("traces-chat-simple", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/copilot/traces-chat-simple.json")
		spans := allSpans(t, td)
		var chats int
		for _, s := range spans {
			if opName(s) == "chat" {
				chats++
				if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.input_tokens"); !ok {
					t.Error("chat span missing gen_ai.usage.input_tokens")
				}
				if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.output_tokens"); !ok {
					t.Error("chat span missing gen_ai.usage.output_tokens")
				}
			}
		}
		if chats != 2 {
			t.Errorf("want 2 chat spans (title-gen + main), got %d", chats)
		}
	})

	t.Run("traces-chat-tools", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/copilot/traces-chat-tools.json")
		var chats, tools int
		for _, s := range allSpans(t, td) {
			switch opName(s) {
			case "chat":
				chats++
			case "execute_tool":
				tools++
			}
		}
		if chats < 2 {
			t.Errorf("want >=2 chat spans, got %d", chats)
		}
		if tools == 0 {
			t.Error("want execute_tool spans")
		}
		for _, s := range allSpans(t, td) {
			if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.reasoning.output_tokens"); ok {
				if _, legacy := attrInt(t, s.Attributes(), "gen_ai.usage.reasoning_tokens"); !legacy {
					t.Error("reasoning.output_tokens present without legacy alias")
				}
			}
		}
	})

	t.Run("traces-legacy-reasoning", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/copilot/traces-legacy-reasoning.json")
		foundLegacy, foundNew := false, false
		for _, s := range allSpans(t, td) {
			if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.reasoning_tokens"); ok {
				foundLegacy = true
			}
			if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.reasoning.output_tokens"); ok {
				foundNew = true
			}
			if _, ok := attrInt(t, s.Attributes(), "gen_ai.usage.cache_creation.input_tokens"); ok {
				_ = ok
			}
		}
		if !foundLegacy {
			t.Error("fixture must contain legacy gen_ai.usage.reasoning_tokens")
		}
		if !foundNew {
			t.Error("fixture must contain gen_ai.usage.reasoning.output_tokens")
		}
	})

	t.Run("traces-invoke-agent", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/copilot/traces-invoke-agent.json")
		var agents, chats int
		for _, s := range allSpans(t, td) {
			switch opName(s) {
			case "invoke_agent":
				agents++
			case "chat":
				chats++
			}
		}
		if agents != 2 {
			t.Errorf("want 2 invoke_agent spans, got %d", agents)
		}
		if chats < 1 {
			t.Errorf("want >=1 chat span, got %d", chats)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		md := decodeMetrics(t, "../../testdata/copilot/metrics.json")
		var tokenUsage bool
		for _, rm := range md.ResourceMetrics().All() {
			for _, sm := range rm.ScopeMetrics().All() {
				for _, m := range sm.Metrics().All() {
					if m.Name() == "gen_ai.client.token.usage" {
						tokenUsage = true
					}
				}
			}
		}
		if !tokenUsage {
			t.Error("metrics fixture missing gen_ai.client.token.usage")
		}
	})

	t.Run("logs", func(t *testing.T) {
		ld := decodeLogs(t, "../../testdata/copilot/logs.json")
		var inferenceEvents int
		for _, rl := range ld.ResourceLogs().All() {
			for _, sl := range rl.ScopeLogs().All() {
				for _, lr := range sl.LogRecords().All() {
					if v, ok := lr.Attributes().Get("event.name"); ok && v.Str() == "gen_ai.client.inference.operation.details" {
						inferenceEvents++
						if _, ok := attrInt(t, lr.Attributes(), "gen_ai.usage.input_tokens"); !ok {
							t.Error("inference event missing input tokens")
						}
					}
				}
			}
		}
		if inferenceEvents == 0 {
			t.Error("logs fixture missing gen_ai.client.inference.operation.details events")
		}
	})
}

func TestDecodeOpenCodeFixtures(t *testing.T) {
	t.Run("traces-llm-json", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/opencode/traces-llm.json")
		spans := allSpans(t, td)
		var llm, session int
		for _, s := range spans {
			switch s.Name() {
			case "opencode.llm":
				llm++
				if v, ok := s.Attributes().Get("openinference.span.kind"); !ok || v.Str() != "LLM" {
					t.Error("opencode.llm span missing openinference.span.kind=LLM")
				}
				if _, ok := attrInt(t, s.Attributes(), "llm.token_count.prompt"); !ok {
					t.Error("missing llm.token_count.prompt")
				}
				if _, ok := attrInt(t, s.Attributes(), "llm.token_count.completion"); !ok {
					t.Error("missing llm.token_count.completion")
				}
				if _, ok := attrInt(t, s.Attributes(), "llm.token_count.completion_details.reasoning"); !ok {
					t.Error("missing llm.token_count.completion_details.reasoning")
				}
				if _, ok := attrInt(t, s.Attributes(), "llm.token_count.prompt_details.cache_read"); !ok {
					t.Error("missing llm.token_count.prompt_details.cache_read")
				}
				if _, ok := attrInt(t, s.Attributes(), "llm.token_count.prompt_details.cache_write"); !ok {
					t.Error("missing llm.token_count.prompt_details.cache_write")
				}
				if v, ok := s.Attributes().Get("llm.cost.total"); !ok || v.Type() != pcommon.ValueTypeDouble {
					t.Error("missing llm.cost.total double")
				}
				if _, ok := s.Attributes().Get("session.id"); !ok {
					t.Error("missing session.id")
				}
			case "opencode.session":
				session++
			}
		}
		if llm != 1 {
			t.Errorf("want 1 opencode.llm span, got %d", llm)
		}
		if session != 1 {
			t.Errorf("want 1 opencode.session span, got %d", session)
		}
	})

	t.Run("traces-llm-pb", func(t *testing.T) {
		data, err := os.ReadFile("../../testdata/opencode/traces-llm.pb")
		if err != nil {
			t.Fatal(err)
		}
		u := &ptrace.ProtoUnmarshaler{}
		td, err := u.UnmarshalTraces(data)
		if err != nil {
			t.Fatalf("decode pb: %v", err)
		}
		if got := len(allSpans(t, td)); got != 2 {
			t.Errorf("want 2 spans in pb fixture, got %d", got)
		}
	})

	t.Run("traces-llm-multiturn", func(t *testing.T) {
		td := decodeTraces(t, "../../testdata/opencode/traces-llm-multiturn.json")
		var found bool
		for _, s := range allSpans(t, td) {
			if s.Name() != "opencode.llm" {
				continue
			}
			found = true
			cacheRead, ok := attrInt(t, s.Attributes(), "llm.token_count.prompt_details.cache_read")
			if !ok || cacheRead <= 0 {
				t.Errorf("multi-turn fixture must show cache_read > 0, got %d", cacheRead)
			}
			if v, ok := s.Attributes().Get("llm.finish_reason"); ok && v.Str() != "tool-calls" {
				t.Errorf("unexpected finish reason %q", v.Str())
			}
		}
		if !found {
			t.Error("fixture missing opencode.llm span")
		}
	})

	t.Run("logs-api-request", func(t *testing.T) {
		ld := decodeLogs(t, "../../testdata/opencode/logs.json")
		var apiRequests int
		for _, rl := range ld.ResourceLogs().All() {
			for _, sl := range rl.ScopeLogs().All() {
				for _, lr := range sl.LogRecords().All() {
					if lr.Body().Str() != "api_request" {
						continue
					}
					apiRequests++
					for _, key := range []string{"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_creation_tokens"} {
						if _, ok := attrInt(t, lr.Attributes(), key); !ok {
							t.Errorf("api_request missing %s", key)
						}
					}
					if v, ok := lr.Attributes().Get("cost_usd"); !ok || v.Type() != pcommon.ValueTypeDouble {
						t.Error("api_request missing cost_usd double")
					}
					if v, ok := lr.Attributes().Get("model"); !ok || v.Str() == "" {
						t.Error("api_request missing model")
					}
				}
			}
		}
		if apiRequests == 0 {
			t.Error("logs fixture missing api_request events")
		}
	})

	t.Run("metrics-token-usage", func(t *testing.T) {
		md := decodeMetrics(t, "../../testdata/opencode/metrics.json")
		var types pcommon.Map
		for _, rm := range md.ResourceMetrics().All() {
			for _, sm := range rm.ScopeMetrics().All() {
				for _, m := range sm.Metrics().All() {
					if m.Name() != "opencode.token.usage" {
						continue
					}
					for d := 0; d < m.Sum().DataPoints().Len(); d++ {
						types = m.Sum().DataPoints().At(d).Attributes()
						if v, ok := types.Get("type"); ok {
							switch v.Str() {
							case "input", "output", "reasoning", "cacheRead", "cacheCreation":
							default:
								t.Errorf("unexpected token type %q", v.Str())
							}
						}
					}
				}
			}
		}
		if types.Len() == 0 {
			t.Error("metrics fixture missing opencode.token.usage data points")
		}
	})
}

func TestFixturesRedacted(t *testing.T) {
	contentKeys := []string{
		"input.value", "output.value", "llm.input_messages", "llm.output_messages",
		"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.definitions",
		"gen_ai.system_instructions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
		"copilot_chat.user_request", "copilot_chat.reasoning_content",
		"exception.message", "exception.stacktrace", "content",
	}
	spanFixtures := []string{
		"../../testdata/copilot/traces-chat-simple.json",
		"../../testdata/copilot/traces-chat-tools.json",
		"../../testdata/copilot/traces-legacy-reasoning.json",
		"../../testdata/copilot/traces-invoke-agent.json",
		"../../testdata/opencode/traces-llm.json",
	}
	for _, path := range spanFixtures {
		td := decodeTraces(t, path)
		for _, s := range allSpans(t, td) {
			for _, key := range contentKeys {
				if v, ok := s.Attributes().Get(key); ok && v.Str() != "[REDACTED]" {
					t.Errorf("%s: content attribute %s not redacted", path, key)
				}
			}
			if msg := s.Status().Message(); msg != "" && msg != "[REDACTED]" {
				t.Errorf("%s: span status message not redacted", path)
			}
		}
	}
}
