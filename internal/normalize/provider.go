package normalize

import "strings"

// UnderlyingProvider derives a friendly provider label from the model ID
// prefix. Display-only (Phase 6 UI); the stored `provider` column always
// keeps the raw source attribute.
func UnderlyingProvider(model string) string {
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
