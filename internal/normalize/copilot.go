package normalize

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// FromCopilotSpan maps a Copilot span to a Generation.
//
// Spans with gen_ai.operation.name "chat" are the only generation records:
// "invoke_agent" spans are session aggregates carrying no per-call usage
// (storing them would double count), and "execute_tool"/"execute_hook"
// spans are tool calls, not LLM usage.
func FromCopilotSpan(resource pcommon.Map, span ptrace.Span) (Generation, bool, error) {
	attrs := span.Attributes()
	if op, _ := attrString(attrs, "gen_ai.operation.name"); op != "chat" {
		return Generation{}, false, nil
	}
	if err := requireStrings(attrs, "gen_ai.provider.name", "gen_ai.request.model", "gen_ai.response.model"); err != nil {
		return Generation{}, true, fmt.Errorf("copilot span %q: %w", span.Name(), err)
	}

	id, err := DedupID(SourceCopilot, span.TraceID().String(), span.SpanID().String())
	if err != nil {
		return Generation{}, true, fmt.Errorf("copilot span %q: %w", span.Name(), err)
	}

	start := span.StartTimestamp().AsTime().UTC()
	end := span.EndTimestamp().AsTime()
	gen := Generation{
		ID:                  id,
		Timestamp:           start,
		Source:              SourceCopilot,
		ServiceName:         serviceName(resource),
		Provider:            firstString(attrs, "gen_ai.provider.name"),
		Model:               firstString(attrs, "gen_ai.response.model", "gen_ai.request.model"),
		ConversationID:      firstString(attrs, "gen_ai.conversation.id"),
		TraceID:             span.TraceID().String(),
		SpanID:              span.SpanID().String(),
		Duration:            end.Sub(start),
		AgentName:           firstString(attrs, "gen_ai.agent.name"),
		GitRepo:             firstString(attrs, "github.copilot.git.repository"),
		GitBranch:           firstString(attrs, "github.copilot.git.branch"),
		InputTokens:         attrIntPtr(attrs, "gen_ai.usage.input_tokens"),
		OutputTokens:        attrIntPtr(attrs, "gen_ai.usage.output_tokens"),
		CacheReadTokens:     attrIntPtr(attrs, "gen_ai.usage.cache_read.input_tokens"),
		CacheCreationTokens: attrIntPtr(attrs, "gen_ai.usage.cache_creation.input_tokens"),
		ReasoningTokens:     attrIntPtr(attrs, "gen_ai.usage.reasoning.output_tokens"),
	}
	if gen.ReasoningTokens == nil {
		// Legacy alias observed simultaneously on real spans; used only
		// when the new attribute is absent.
		gen.ReasoningTokens = attrIntPtr(attrs, "gen_ai.usage.reasoning_tokens")
	}
	// Copilot telemetry carries no cost attribute; Cost is never computed.
	gen.Cost = nil
	return gen, true, nil
}
