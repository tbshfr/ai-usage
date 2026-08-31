package normalize

import (
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	SourceCopilot  = "copilot"
	SourceOpenCode = "opencode"
)

// Generation is the canonical usage record (single source of truth, see
// docs/plans/README.md). Nullable fields stay nil when the source did not
// report the value; missing is never coerced to zero.
type Generation struct {
	ID                  string
	Timestamp           time.Time
	Source              string
	ServiceName         string
	Provider            string
	Model               string
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64
	ReasoningTokens     *int64
	Cost                *float64
	ConversationID      string
	TraceID             string
	SpanID              string
	Duration            time.Duration
	AgentName           string
	GitRepo             string
	GitBranch           string
}

// FromSpan normalizes a span for the given source. Returns ok=false for
// spans that are legitimately not generation records (aggregates, tool
// calls), and an error for malformed generation spans.
func FromSpan(source string, resource pcommon.Map, span ptrace.Span) (Generation, bool, error) {
	switch source {
	case SourceCopilot:
		return FromCopilotSpan(resource, span)
	case SourceOpenCode:
		return FromOpenCodeSpan(resource, span)
	default:
		return Generation{}, false, fmt.Errorf("unknown source %q", source)
	}
}

// DetectSource classifies a span's owning resource. Resource service.name
// wins; the span-attribute fallback catches copilot payloads that arrive
// under an unexpected service name.
func DetectSource(resource, spanAttrs pcommon.Map) string {
	if sn, ok := attrString(resource, "service.name"); ok {
		switch sn {
		case "copilot-chat", "github-copilot":
			return SourceCopilot
		case "opencode":
			return SourceOpenCode
		}
	}
	if hasPrefixKey(spanAttrs, "github.copilot.") {
		return SourceCopilot
	}
	return ""
}

func hasPrefixKey(m pcommon.Map, prefix string) bool {
	found := false
	m.Range(func(k string, _ pcommon.Value) bool {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			found = true
			return false
		}
		return true
	})
	return found
}

func serviceName(resource pcommon.Map) string {
	name, _ := attrString(resource, "service.name")
	return name
}

func attrString(m pcommon.Map, key string) (string, bool) {
	v, ok := m.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

func firstString(m pcommon.Map, keys ...string) string {
	for _, k := range keys {
		if v, ok := attrString(m, k); ok {
			return v
		}
	}
	return ""
}

// attrInt tolerates exporters that send numbers as doubles or strings.
func attrInt(m pcommon.Map, key string) (int64, bool) {
	v, ok := m.Get(key)
	if !ok {
		return 0, false
	}
	switch v.Type() {
	case pcommon.ValueTypeInt:
		return v.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(v.Double()), true
	case pcommon.ValueTypeStr:
		n, err := strconv.ParseInt(v.Str(), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func attrDouble(m pcommon.Map, key string) (float64, bool) {
	v, ok := m.Get(key)
	if !ok {
		return 0, false
	}
	switch v.Type() {
	case pcommon.ValueTypeDouble:
		return v.Double(), true
	case pcommon.ValueTypeInt:
		return float64(v.Int()), true
	case pcommon.ValueTypeStr:
		f, err := strconv.ParseFloat(v.Str(), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func attrIntPtr(m pcommon.Map, key string) *int64 {
	if n, ok := attrInt(m, key); ok {
		return &n
	}
	return nil
}

func attrDoublePtr(m pcommon.Map, key string) *float64 {
	if f, ok := attrDouble(m, key); ok {
		return &f
	}
	return nil
}
