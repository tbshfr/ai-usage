package normalize

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// FromOpenCodeSpan maps an opencode span to a Generation. Per D1 the truth
// signal is the `opencode.llm` span (openinference.span.kind=LLM); session
// spans are aggregates and logs/metrics are never generation records.
func FromOpenCodeSpan(resource pcommon.Map, span ptrace.Span) (Generation, bool, error) {
	attrs := span.Attributes()
	kind, _ := attrString(attrs, "openinference.span.kind")
	if kind != "LLM" && span.Name() != "opencode.llm" {
		return Generation{}, false, nil
	}
	if err := requireStrings(attrs, "gen_ai.provider.name", "llm.system", "llm.model_name"); err != nil {
		return Generation{}, true, fmt.Errorf("opencode span %q: %w", span.Name(), err)
	}

	id, err := DedupID(SourceOpenCode, span.TraceID().String(), span.SpanID().String())
	if err != nil {
		return Generation{}, true, fmt.Errorf("opencode span %q: %w", span.Name(), err)
	}

	start := span.StartTimestamp().AsTime().UTC()
	end := span.EndTimestamp().AsTime()
	gen := Generation{
		ID:                  id,
		Timestamp:           start,
		Source:              SourceOpenCode,
		ServiceName:         serviceName(resource),
		Provider:            firstString(attrs, "gen_ai.provider.name", "llm.system"),
		Model:               firstString(attrs, "llm.model_name"),
		ConversationID:      firstString(attrs, "session.id"),
		AgentName:           firstString(attrs, "agent.name"),
		TraceID:             span.TraceID().String(),
		SpanID:              span.SpanID().String(),
		Duration:            end.Sub(start),
		InputTokens:         attrIntPtr(attrs, "llm.token_count.prompt"),
		OutputTokens:        attrIntPtr(attrs, "llm.token_count.completion"),
		CacheReadTokens:     attrIntPtr(attrs, "llm.token_count.prompt_details.cache_read"),
		CacheCreationTokens: attrIntPtr(attrs, "llm.token_count.prompt_details.cache_write"),
		ReasoningTokens:     attrIntPtr(attrs, "llm.token_count.completion_details.reasoning"),
		Cost:                attrDoublePtr(attrs, "llm.cost.total"),
	}
	if gen.Cost == nil {
		gen.Cost = attrDoublePtr(attrs, "cost_usd")
	}
	return gen, true, nil
}
