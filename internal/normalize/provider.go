package normalize

import "strings"

// ModelCreator derives a friendly creator (vendor) label from the
// model ID prefix. Display-only; the stored `provider` column always keeps
// the raw source attribute (with Codex as the one derived exception).
func ModelCreator(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return "openai"
	case strings.HasPrefix(m, "gemini"):
		return "google"
	case strings.HasPrefix(m, "grok"):
		return "xai"
	default:
		return "unknown"
	}
}
