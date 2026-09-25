package normalize

import (
	"testing"
	"time"
)

func TestFromCopilotSpanFixtures(t *testing.T) {
	t.Run("traces-chat-simple", func(t *testing.T) {
		spans := loadSpans(t, "../../testdata/copilot/traces-chat-simple.json")
		var gens []Generation
		for _, ns := range spans {
			gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("span %q should be a chat span", ns.span.Name())
			}
			gens = append(gens, gen)
		}
		if len(gens) != 2 {
			t.Fatalf("want 2 generations, got %d", len(gens))
		}

		title, main := gens[0], gens[1]
		if title.Model != "gpt-4o-mini-2024-07-18" || main.Model != "gpt-5.6-luna" {
			t.Errorf("models = %q, %q", title.Model, main.Model)
		}
		if title.Provider != "github" || main.Provider != "github" {
			t.Errorf("providers = %q, %q", title.Provider, main.Provider)
		}
		if title.InputTokens == nil || *title.InputTokens != 260 || *title.OutputTokens != 4 {
			t.Errorf("title tokens = %v/%v", title.InputTokens, title.OutputTokens)
		}
		if main.InputTokens == nil || *main.InputTokens != 15299 || *main.OutputTokens != 116 {
			t.Errorf("main tokens = %v/%v", main.InputTokens, main.OutputTokens)
		}
		if title.CacheReadTokens == nil || *title.CacheReadTokens != 0 {
			t.Errorf("title cache_read = %v, want explicit 0", title.CacheReadTokens)
		}
		if title.AgentName != "title" {
			t.Errorf("title agent = %q", title.AgentName)
		}
		if main.ConversationID != "d1b30fef-089e-481b-b33a-e619c6f59ec1" {
			t.Errorf("main conversation = %q", main.ConversationID)
		}
		for _, gen := range gens {
			if gen.Cost != nil {
				t.Errorf("copilot cost must always be nil, got %v", *gen.Cost)
			}
			if gen.Source != SourceCopilot || gen.ServiceName != "copilot-chat" {
				t.Errorf("source/service = %q/%q", gen.Source, gen.ServiceName)
			}
			if gen.Timestamp.Location() != time.UTC {
				t.Errorf("timestamp not UTC: %v", gen.Timestamp)
			}
			if gen.ID == "" || gen.TraceID == "" || gen.SpanID == "" {
				t.Error("missing IDs")
			}
		}
		if main.Duration <= 0 {
			t.Errorf("duration = %v, want positive", main.Duration)
		}
	})

	t.Run("traces-chat-tools", func(t *testing.T) {
		spans := loadSpans(t, "../../testdata/copilot/traces-chat-tools.json")
		var gens []Generation
		for _, ns := range spans {
			gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue // execute_tool spans
			}
			gens = append(gens, gen)
		}
		if len(gens) != 2 {
			t.Fatalf("want 2 chat generations, got %d", len(gens))
		}
		for i, want := range []int64{276, 86} {
			gen := gens[i]
			if gen.Model != "claude-haiku-4-5-20251001" {
				t.Errorf("gen %d model = %q", i, gen.Model)
			}
			if gen.ReasoningTokens == nil || *gen.ReasoningTokens != want {
				t.Errorf("gen %d reasoning = %v, want %d", i, gen.ReasoningTokens, want)
			}
			if gen.CacheCreationTokens == nil || *gen.CacheCreationTokens == 0 {
				t.Errorf("gen %d cache_creation missing", i)
			}
		}
		if gens[0].ID == gens[1].ID {
			t.Error("distinct chat spans in one trace must get distinct IDs")
		}
	})

	t.Run("traces-legacy-reasoning", func(t *testing.T) {
		spans := loadSpans(t, "../../testdata/copilot/traces-legacy-reasoning.json")
		var found int
		for _, ns := range spans {
			gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue
			}
			found++
			if gen.ReasoningTokens == nil || *gen.ReasoningTokens != 40 {
				t.Errorf("reasoning = %v, want 40 (legacy alias present)", gen.ReasoningTokens)
			}
		}
		if found != 1 {
			t.Fatalf("want 1 generation, got %d", found)
		}
	})

	t.Run("traces-invoke-agent", func(t *testing.T) {
		spans := loadSpans(t, "../../testdata/copilot/traces-invoke-agent.json")
		var chats, aggregates int
		for _, ns := range spans {
			gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case ok:
				chats++
				if gen.Model != "claude-haiku-4-5-20251001" && gen.Model != "gpt-5.6-luna" {
					t.Errorf("unexpected model %q", gen.Model)
				}
				if ns.span.Name() == "chat claude-haiku-4.5" && gen.GitBranch != "" {
					// git.branch lives on the invoke_agent span, not the chat span
					t.Errorf("chat span should not inherit git branch")
				}
			default:
				aggregates++ // invoke_agent + execute_tool spans yield nothing
			}
		}
		if chats != 2 || aggregates != 3 {
			t.Errorf("chats = %d, aggregates = %d, want 2/3", chats, aggregates)
		}
	})
}

func TestFromCopilotSpanLegacyOnlyReasoning(t *testing.T) {
	ns := syntheticSpan(map[string]any{
		"gen_ai.operation.name":         "chat",
		"gen_ai.usage.reasoning_tokens": int64(15),
	}, "copilot-chat")

	gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.ReasoningTokens == nil || *gen.ReasoningTokens != 15 {
		t.Errorf("reasoning = %v, want legacy alias value 15", gen.ReasoningTokens)
	}
}

func TestFromCopilotSpanBYOKProvider(t *testing.T) {
	for _, tc := range []struct {
		name, reported, address, want string
	}{
		{"Anthropic BYOK reported", "anthropic", "api.anthropic.com", "anthropic"},
		{"Gemini BYOK reported", "gemini", "generativelanguage.googleapis.com", "gemini"},
		{"Anthropic BYOK hostname fallback", "github", "api.anthropic.com", "anthropic"},
		{"Gemini BYOK hostname fallback", "github", "generativelanguage.googleapis.com", "gemini"},
		{"OpenRouter BYOK", "github", "openrouter.ai", "openrouter"},
		{"OpenRouter hostname case", "github", "OpenRouter.AI.", "openrouter"},
		{"OpenAI BYOK", "github", "api.openai.com", "openai"},
		{"xAI BYOK", "github", "api.x.ai", "xai"},
		{"Azure OpenAI BYOK", "github", "my-resource.openai.azure.com", "azure"},
		{"Azure Models BYOK", "github", "models.ai.azure.com", "azure"},
		{"Azure Foundry BYOK", "github", "my-resource.services.ai.azure.com", "azure"},
		{"Azure serverless BYOK", "github", "my-model.eastus.inference.ai.azure.com", "azure"},
		{"Azure inference BYOK", "github", "my-resource.eastus.inference.ml.azure.com", "azure"},
		{"GitHub", "github", "api.githubcopilot.com", "github"},
		{"GitHub regional endpoint", "github", "api.business.githubcopilot.com", "github"},
		{"GitHub apex is not inference", "github", "github.com", "custom"},
		{"GitHub API is not inference", "github", "api.github.com", "custom"},
		{"GitHub legacy endpoint", "github", "copilot-proxy.githubusercontent.com", "github"},
		{"GitHub content endpoint is not inference", "github", "avatars.githubusercontent.com", "custom"},
		{"GitHub assets endpoint is not inference", "github", "github.githubassets.com", "custom"},
		{"GitHub Enterprise endpoint", "github", "copilot-proxy.company.ghe.com", "github"},
		{"GitHub Enterprise root is not inference", "github", "company.ghe.com", "custom"},
		{"missing hostname", "github", "", "github"},
		{"missing provider with known hostname", "", "api.anthropic.com", "anthropic"},
		{"missing provider with Azure hostname", "", "my-resource.services.ai.azure.com", "azure"},
		{"missing provider with GitHub hostname", "", "api.githubcopilot.com", "github"},
		{"missing provider with localhost", "", "localhost", "local"},
		{"missing provider with custom hostname", "", "models.example.com", "custom"},
		{"missing provider and hostname", "", "", ""},
		{"custom endpoint", "github", "models.example.com", "custom"},
		{"Ollama localhost endpoint", "github", "localhost", "local"},
		{"localhost hostname case", "github", "LocalHost.", "local"},
		{"localhost subdomain", "github", "models.localhost", "local"},
		{"IPv4 loopback endpoint", "github", "127.0.0.1", "local"},
		{"IPv4 loopback alias", "github", "127.0.0.2", "local"},
		{"IPv6 loopback endpoint", "github", "[::1]", "local"},
		{"explicit provider on local endpoint", "openai", "localhost", "openai"},
		{"OpenCode provider on local endpoint", "opencode", "localhost", "opencode"},
		{"OpenCode provider on IPv4 loopback", "opencode", "127.0.0.1", "opencode"},
		{"non-loopback IP endpoint", "github", "192.168.1.10", "custom"},
		{"lookalike OpenRouter hostname", "github", "not-openrouter.ai", "custom"},
		{"lookalike Azure hostname", "github", "not-openai.azure.com", "custom"},
		{"lookalike GitHub hostname", "github", "notgithub.com", "custom"},
		{"GitHub suffix lookalike", "github", "api.githubcopilot.com.example.org", "custom"},
		{"reported non-GitHub provider", "anthropic", "api.anthropic.com", "anthropic"},
		{"reported provider takes priority", "anthropic", "openrouter.ai", "anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]any{
				"gen_ai.operation.name": "chat",
				"gen_ai.request.model":  "openai/gpt-4o",
				"gen_ai.response.model": "openai/gpt-4o",
				"server.address":        tc.address,
			}
			if tc.reported != "" {
				attrs["gen_ai.provider.name"] = tc.reported
			}
			ns := syntheticSpan(attrs, "copilot-chat")
			source := DetectSource(ns.resource, ns.span.Attributes())
			if source != SourceCopilot {
				t.Fatalf("source = %q, want %q", source, SourceCopilot)
			}
			gen, ok, err := FromSpan(source, ns.resource, ns.span)
			if err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if gen.Provider != tc.want {
				t.Errorf("provider = %q, want %q", gen.Provider, tc.want)
			}
		})
	}
}

func TestFromCopilotSpanXtabClearsConversation(t *testing.T) {
	ns := syntheticSpan(map[string]any{
		"gen_ai.operation.name":  "chat",
		"gen_ai.provider.name":   "github",
		"gen_ai.request.model":   "copilot-nes-lysithea-14",
		"gen_ai.response.model":  "copilot-nes-lysithea-14",
		"gen_ai.conversation.id": "conv-xtab-1",
		"gen_ai.agent.name":      AgentXtabProvider,
	}, "copilot-chat")

	gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.AgentName != AgentXtabProvider {
		t.Errorf("agent = %q, want %q", gen.AgentName, AgentXtabProvider)
	}
	if gen.ConversationID != "" {
		t.Errorf("xtab conversation = %q, want empty (per-day Autocomplete group)", gen.ConversationID)
	}
}

func TestFromCopilotSpanSessionlessAgentsClearConversation(t *testing.T) {
	for _, agent := range []string{AgentTitle, AgentProgressMessages} {
		ns := syntheticSpan(map[string]any{
			"gen_ai.operation.name":  "chat",
			"gen_ai.provider.name":   "github",
			"gen_ai.request.model":   "gpt-4o-mini-2024-07-18",
			"gen_ai.response.model":  "gpt-4o-mini-2024-07-18",
			"gen_ai.conversation.id": "conv-parent",
			"gen_ai.agent.name":      agent,
		}, "copilot-chat")

		gen, ok, err := FromCopilotSpan(ns.resource, ns.span)
		if err != nil || !ok {
			t.Fatalf("agent %q: ok=%v err=%v", agent, ok, err)
		}
		if gen.ConversationID != "" {
			t.Errorf("agent %q conversation = %q, want empty (per-day Title/progress group)", agent, gen.ConversationID)
		}
		if !IsSessionlessAgent(agent) {
			t.Errorf("IsSessionlessAgent(%q) = false, want true", agent)
		}
	}
	if IsSessionlessAgent("panel/editAgent") {
		t.Error(`IsSessionlessAgent("panel/editAgent") = true, want false`)
	}
}
