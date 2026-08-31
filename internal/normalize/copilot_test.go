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
