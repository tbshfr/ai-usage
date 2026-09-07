package normalize

import "testing"

// Contract tests for ModelCreator. Intentionally NOT an exhaustive
// per-vendor table: adding a new model prefix must not require a test
// update. Only the stable contract is asserted here:
//
//   - unrecognized models fall back to "unknown"
//   - matching is case-insensitive
//   - the result is never empty
//
// Per-vendor spot checks (if any) live here as smoke tests for long-lived
// mappings only; do not add every new prefix to the test suite.
func TestModelCreatorFallback(t *testing.T) {
	cases := []string{
		"mystery-model-9000",
		"custom-finetune-v2",
		"",
	}
	for _, model := range cases {
		if got := ModelCreator(model); got != "unknown" {
			t.Errorf("ModelCreator(%q) = %q, want %q", model, got, "unknown")
		}
	}
}

func TestModelCreatorCaseInsensitive(t *testing.T) {
	if got := ModelCreator("CLAUDE-haiku-4.5"); got != "anthropic" {
		t.Errorf("ModelCreator(%q) = %q, want %q", "CLAUDE-haiku-4.5", got, "anthropic")
	}
	if got := ModelCreator("GPT-5.6-LUNA"); got != "openai" {
		t.Errorf("ModelCreator(%q) = %q, want %q", "GPT-5.6-LUNA", got, "openai")
	}
}

// Regression test for the over-broad "hy" prefix: bare Hunyuan models are
// "hy<version>" (hy4-preview today, hy5+ tomorrow), but "hyper-*"/"hybrid-*"
// must stay "unknown".
func TestModelCreatorTencentHunyuan(t *testing.T) {
	wantTencent := []string{
		"hy4-preview",
		"hy5",
		"HY5-preview",
		"tencent/hy4-preview",
		"hunyuan-turbos-latest",
	}
	for _, model := range wantTencent {
		if got := ModelCreator(model); got != "tencent" {
			t.Errorf("ModelCreator(%q) = %q, want %q", model, got, "tencent")
		}
	}
	wantUnknown := []string{
		"hyper-model",
		"hybrid-foo",
		"hype",
	}
	for _, model := range wantUnknown {
		if got := ModelCreator(model); got != "unknown" {
			t.Errorf("ModelCreator(%q) = %q, want %q", model, got, "unknown")
		}
	}
}

func TestModelCreatorNeverEmpty(t *testing.T) {
	models := []string{
		"",
		"mystery-model-9000",
		"claude-haiku-4.5",
		"gpt-5.6-luna",
		"o3-mini",
		"gemini-3-flash",
		"grok-4",
		"z-ai/glm-5.3-flash",
		"some-unknown-model",
	}
	for _, model := range models {
		if got := ModelCreator(model); got == "" {
			t.Errorf("ModelCreator(%q) = empty, want non-empty creator label", model)
		}
	}
}
