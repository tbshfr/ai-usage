package normalize

import (
	"fmt"
	"net"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Session-less VS Code agents: no meaningful conversation, grouped per day
// in the sessions overview. XtabProvider is inline autocomplete (Next Edit
// Suggestions); title and progressMessages are chat helper generations.
const (
	AgentXtabProvider     = "XtabProvider"
	AgentTitle            = "title"
	AgentProgressMessages = "progressMessages"
)

// IsSessionlessAgent reports whether a Copilot agent name carries no
// meaningful session (autocomplete plus title/progress helpers).
func IsSessionlessAgent(agent string) bool {
	switch agent {
	case AgentXtabProvider, AgentTitle, AgentProgressMessages:
		return true
	}
	return false
}

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
		Provider:            copilotProvider(attrs),
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
	// Copilot telemetry carries no cost attribute; pricing enrichment runs after storage.
	gen.Cost = nil
	if IsSessionlessAgent(gen.AgentName) {
		// Autocomplete and title/progress helpers have no session: ignore
		// any reported conversation so they group per day.
		gen.ConversationID = ""
	}
	return gen, true, nil
}

// Copilot's shared chat fetcher reports "github" even for OpenAI-compatible
// BYOK endpoints. Trust a specific reported provider before interpreting the
// endpoint for the shared fetcher's "github" label or a missing provider.
func copilotProvider(attrs pcommon.Map) string {
	provider := firstString(attrs, "gen_ai.provider.name")
	host := strings.ToLower(strings.TrimSuffix(firstString(attrs, "server.address"), "."))
	if provider != "" && !strings.EqualFold(provider, "github") {
		return provider
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "local"
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsLoopback() {
		return "local"
	}
	switch host {
	case "api.anthropic.com":
		return "anthropic"
	case "generativelanguage.googleapis.com":
		return "gemini"
	case "openrouter.ai":
		return "openrouter"
	case "api.openai.com":
		return "openai"
	case "api.x.ai":
		return "xai"
	case "models.ai.azure.com":
		return "azure"
	case "":
		return provider
	}
	if strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".services.ai.azure.com") ||
		strings.HasSuffix(host, ".inference.ai.azure.com") ||
		strings.HasSuffix(host, ".inference.ml.azure.com") {
		return "azure"
	}
	if isCopilotInferenceHost(host) {
		return "github"
	}
	return "custom"
}

// Only Copilot inference hosts justify retaining "github" for a chat span.
func isCopilotInferenceHost(host string) bool {
	return host == "copilot-proxy.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubcopilot.com") ||
		(strings.HasPrefix(host, "copilot-proxy.") && strings.HasSuffix(host, ".ghe.com"))
}
