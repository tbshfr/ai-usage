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
	case strings.HasPrefix(m, "muse"):
		return "meta"
	case strings.HasPrefix(m, "glm-"), strings.HasPrefix(m, "z-ai/"):
		return "zhipu"
	case strings.HasPrefix(m, "nemotron-"):
		return "nvidia"
	case strings.HasPrefix(m, "mai-"), strings.HasPrefix(m, "copilot-"):
		return "microsoft"
	case isTencentHunyuan(m):
		return "tencent"
	case strings.HasPrefix(m, "ling-"):
		return "inclusionai"
	case strings.HasPrefix(m, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(m, "kimi"):
		return "moonshot"
	case strings.HasPrefix(m, "mimo"):
		return "xiaomi"
	case strings.HasPrefix(m, "qwen"):
		return "alibaba"
	default:
		return "unknown"
	}
}

// isTencentHunyuan matches Tencent Hunyuan models sent as
// "tencent/hy4-preview" or bare "hy4-preview", including future major
// versions (hy5, hy6, ...). The bare form requires a digit after "hy" so
// unrelated "hyper-*"/"hybrid-*" models don't match.
func isTencentHunyuan(m string) bool {
	if strings.HasPrefix(m, "tencent/") || strings.HasPrefix(m, "hunyuan") {
		return true
	}
	return len(m) >= 3 && m[0] == 'h' && m[1] == 'y' && m[2] >= '0' && m[2] <= '9'
}
