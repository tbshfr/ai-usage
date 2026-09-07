package normalize

import "testing"

// Phase 7 item 7: model strings that match no display-provider prefix fall
// back to "unknown"; the raw model/provider stays in the row.
func TestModelCreatorFallback(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-haiku-4.5", "anthropic"},
		{"gpt-5.6-luna", "openai"},
		{"o3-mini", "openai"},
		{"gemini-3-flash", "google"},
		{"grok-4", "xai"},
		{"mystery-model-9000", "unknown"},
		{"custom-finetune-v2", "unknown"},
		{"", "unknown"},
	}
	for _, c := range cases {
		if got := ModelCreator(c.model); got != c.want {
			t.Errorf("ModelCreator(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}
