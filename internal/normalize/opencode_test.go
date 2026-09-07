package normalize

import (
	"testing"
)

func TestFromOpenCodeSpanFixtures(t *testing.T) {
	t.Run("traces-llm", func(t *testing.T) {
		spans := loadSpans(t, "../../testdata/opencode/traces-llm.json")
		var gens []Generation
		var skipped int
		for _, ns := range spans {
			gen, ok, err := FromOpenCodeSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				skipped++ // opencode.session aggregate
				continue
			}
			gens = append(gens, gen)
		}
		if skipped != 1 || len(gens) != 1 {
			t.Fatalf("gens=%d skipped=%d, want 1/1", len(gens), skipped)
		}
		gen := gens[0]
		if gen.Source != SourceOpenCode || gen.ServiceName != "opencode" {
			t.Errorf("source/service = %q/%q", gen.Source, gen.ServiceName)
		}
		if gen.Model != "z-ai/glm-5.3-flash" {
			t.Errorf("model = %q", gen.Model)
		}
		if gen.Provider != "openrouter" {
			t.Errorf("provider = %q", gen.Provider)
		}
		if gen.InputTokens == nil || *gen.InputTokens != 8212 {
			t.Errorf("input = %v", gen.InputTokens)
		}
		if gen.OutputTokens == nil || *gen.OutputTokens != 3 {
			t.Errorf("output = %v", gen.OutputTokens)
		}
		if gen.ReasoningTokens == nil || *gen.ReasoningTokens != 0 {
			t.Errorf("reasoning = %v, want explicit 0", gen.ReasoningTokens)
		}
		if gen.CacheReadTokens == nil || *gen.CacheReadTokens != 64 {
			t.Errorf("cache_read = %v, want 64", gen.CacheReadTokens)
		}
		if gen.CacheCreationTokens == nil || *gen.CacheCreationTokens != 0 {
			t.Errorf("cache_write = %v, want explicit 0", gen.CacheCreationTokens)
		}
		if gen.Cost == nil || *gen.Cost != 0.00061761 {
			t.Errorf("cost = %v, want 0.00061761 passthrough", gen.Cost)
		}
		if gen.ConversationID != "ses_fac3fa8eaffeoO6k1Mhzqggc1F" {
			t.Errorf("conversation = %q", gen.ConversationID)
		}
		if gen.AgentName != "plan" {
			t.Errorf("agent = %q", gen.AgentName)
		}
		if gen.Duration.Milliseconds() != 4495 {
			t.Errorf("duration = %v, want 4495ms", gen.Duration)
		}
	})

	t.Run("traces-llm-nocache", func(t *testing.T) {
		for _, ns := range loadSpans(t, "../../testdata/opencode/traces-llm-nocache.json") {
			gen, ok, err := FromOpenCodeSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue
			}
			if gen.CacheReadTokens == nil || *gen.CacheReadTokens != 0 {
				t.Errorf("cache_read = %v, want explicit 0", gen.CacheReadTokens)
			}
			if gen.Cost == nil || *gen.Cost != 0.00062145 {
				t.Errorf("cost = %v", gen.Cost)
			}
		}
	})

	t.Run("traces-llm-multiturn", func(t *testing.T) {
		for _, ns := range loadSpans(t, "../../testdata/opencode/traces-llm-multiturn.json") {
			gen, ok, err := FromOpenCodeSpan(ns.resource, ns.span)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue
			}
			if gen.CacheReadTokens == nil || *gen.CacheReadTokens != 3520 {
				t.Errorf("cache_read = %v, want 3520", gen.CacheReadTokens)
			}
			if gen.ReasoningTokens == nil || *gen.ReasoningTokens != 23 {
				t.Errorf("reasoning = %v, want 23", gen.ReasoningTokens)
			}
			if gen.OutputTokens == nil || *gen.OutputTokens != 40 {
				t.Errorf("output = %v, want 40", gen.OutputTokens)
			}
		}
	})

	t.Run("pb-fixture-same-id", func(t *testing.T) {
		jsonSpans := loadSpans(t, "../../testdata/opencode/traces-llm.json")
		pbSpans := loadSpans(t, "../../testdata/opencode/traces-llm.pb")
		jsonGen, ok, err := FromOpenCodeSpan(jsonSpans[0].resource, jsonSpans[0].span)
		if err != nil || !ok {
			t.Fatalf("json span: ok=%v err=%v", ok, err)
		}
		pbGen, ok, err := FromOpenCodeSpan(pbSpans[0].resource, pbSpans[0].span)
		if err != nil || !ok {
			t.Fatalf("pb span: ok=%v err=%v", ok, err)
		}
		if jsonGen.ID != pbGen.ID {
			t.Error("same span in JSON and protobuf encoding must produce identical dedup ID")
		}
	})
}

func TestFromOpenCodeSpanProviderFallback(t *testing.T) {
	ns := syntheticSpan(map[string]any{
		"openinference.span.kind": "LLM",
		"llm.system":              "anthropic",
		"llm.model_name":          "claude-haiku-4.5",
	}, "opencode")

	gen, ok, err := FromOpenCodeSpan(ns.resource, ns.span)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gen.Provider != "anthropic" {
		t.Errorf("provider = %q, want llm.system fallback", gen.Provider)
	}
	if gen.Cost != nil {
		t.Errorf("cost = %v, want nil when unreported", gen.Cost)
	}
}

func TestModelCreator(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4.5":   "anthropic",
		"gpt-5.6-luna":       "openai",
		"o3-mini":            "openai",
		"gemini-2.0-flash":   "google",
		"grok-3":             "xai",
		"z-ai/glm-5.3-flash": "unknown",
		"some-unknown-model": "unknown",
		"":                   "unknown",
	}
	for model, want := range cases {
		if got := ModelCreator(model); got != want {
			t.Errorf("ModelCreator(%q) = %q, want %q", model, got, want)
		}
	}
}
