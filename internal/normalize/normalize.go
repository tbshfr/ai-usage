package normalize

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	SourceCopilot    = "copilot"
	SourceOpenCode   = "opencode"
	SourceCodex      = "codex"
	SourceMaki       = "maki"
	SourceClaudeCode = "claude-code"
)

// Generation is the canonical usage record. Nullable fields stay nil when the
// source did not report the value; missing is never coerced to zero.
type Generation struct {
	ID                    string
	Timestamp             time.Time
	Source                string
	ServiceName           string
	Provider              string
	Model                 string
	InputTokens           *int64
	OutputTokens          *int64
	CacheReadTokens       *int64
	CacheCreationTokens   *int64
	ReasoningTokens       *int64
	Cost                  *float64
	CostReportedByHarness bool
	CostSource            string
	PricingModelID        string
	PricingFetchedAt      *time.Time
	// PricingRates is loaded from the shared price snapshot so later telemetry
	// can be priced with the original rates.
	PricingRates    string
	PricingRevision int64
	ConversationID  string
	TraceID         string
	SpanID          string
	Duration        time.Duration
	AgentName       string
	GitRepo         string
	GitBranch       string
	// ReasoningEffort is the reasoning-effort setting the harness reported
	// for the call, such as "medium". Empty when the source does not report it.
	ReasoningEffort string
}

// UncachedInput returns the canonical prompt input with cached tokens
// removed: Copilot and Codex report the prompt count including cached tokens
// (OpenAI-style), so the cached parts are subtracted there; OpenCode, Maki,
// and Claude Code report input separately from cache. Nil stays nil when input was not reported.
// This mirrors the storage layer's uncachedInputSQL so single records and
// aggregates agree; the stored value stays as reported.
func (g Generation) UncachedInput() *int64 {
	if g.InputTokens == nil {
		return nil
	}
	if g.Source != SourceCopilot && g.Source != SourceCodex {
		v := *g.InputTokens
		return &v
	}
	cached := int64(0)
	if g.CacheReadTokens != nil {
		cached += *g.CacheReadTokens
	}
	if g.CacheCreationTokens != nil {
		cached += *g.CacheCreationTokens
	}
	u := *g.InputTokens - cached
	if u < 0 {
		u = 0
	}
	return &u
}

// NonReasoningOutput returns the mutually exclusive output-token bucket used
// for totals. Copilot and Codex report reasoning tokens as a subset of output
// tokens; OpenCode reports separate output and reasoning buckets.
// The stored OutputTokens value remains exactly as reported by the source.
func (g Generation) NonReasoningOutput() *int64 {
	if g.OutputTokens == nil {
		return nil
	}
	v := *g.OutputTokens
	if (g.Source == SourceCopilot || g.Source == SourceCodex) && g.ReasoningTokens != nil {
		v -= *g.ReasoningTokens
		if v < 0 {
			v = 0
		}
	}
	return &v
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
	case SourceCodex:
		// Codex traces include aggregate/session and transport spans, but the
		// authoritative per-response token counts are emitted as logs.
		return Generation{}, false, nil
	default:
		return Generation{}, false, fmt.Errorf("unknown source %q", source)
	}
}

// FromLog normalizes one OTLP log record for the given source. Returns
// ok=false for records that are not terminal generation usage events.
func FromLog(source string, resource pcommon.Map, lr plog.LogRecord) (Generation, bool, error) {
	switch source {
	case SourceCodex:
		return FromCodexLog(resource, lr)
	case SourceMaki:
		return FromMakiLog(resource, lr)
	case SourceClaudeCode:
		return FromClaudeCodeLog(resource, lr)
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
		case "codex_cli_rs":
			return SourceCodex
		}
		if strings.HasPrefix(strings.ToLower(sn), "codex") {
			return SourceCodex
		}
	}
	if hasPrefixKey(spanAttrs, "github.copilot.") {
		return SourceCopilot
	}
	if hasPrefixKey(spanAttrs, "codex.") {
		return SourceCodex
	}
	return ""
}

// DetectLogSource classifies supported log producers from resource metadata.
func DetectLogSource(resource pcommon.Map) string {
	if firstString(resource, "telemetry.sdk.name") == "maki-otel" {
		return SourceMaki
	}
	service := strings.ToLower(firstString(resource, "service.name"))
	if service == "maki" {
		return SourceMaki
	}
	if service == "claude-code" {
		return SourceClaudeCode
	}
	if service == "codex_cli_rs" || strings.HasPrefix(service, "codex") {
		return SourceCodex
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
	return truncateLabel(name)
}

// maxTokensPerCall and maxCostUSD bound single-span values so one crafted
// OTLP span cannot poison SUM() aggregates or JSON encoding for all readers.
// Real single LLM calls are orders of magnitude smaller; out-of-range values
// are treated as missing (nil), never stored.
const (
	maxTokensPerCall = int64(1_000_000_000)
	maxCostUSD       = 1_000_000.0
)

// maxLabelLen caps free-form telemetry strings (model, provider,
// conversation, agent, repo, branch) so one writer cannot permanently inflate
// dropdowns, GROUP BYs, and page sizes for all readers. The cap is a byte
// budget; truncation stays on a rune boundary so multi-byte labels never
// store invalid UTF-8.
const maxLabelLen = 256

func truncateLabel(s string) string {
	if len(s) <= maxLabelLen {
		return s
	}
	b := s[:maxLabelLen]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

func attrString(m pcommon.Map, key string) (string, bool) {
	v, ok := m.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

// ErrNonStringAttrs marks corrupt input where a known attribute key is
// present with a non-string value. Callers classify it to count
// normalization errors by reason.
var ErrNonStringAttrs = errors.New("non-string attribute values")

// requireStrings errors when any key is present with a non-string value.
// attrString treats such values as missing, which would silently store
// generation records with dropped fields; wrong-typed attributes are corrupt
// input and must surface as normalization errors instead.
func requireStrings(m pcommon.Map, keys ...string) error {
	var bad []string
	for _, k := range keys {
		if v, ok := m.Get(k); ok && v.Type() != pcommon.ValueTypeStr {
			bad = append(bad, fmt.Sprintf("%s (%s)", k, v.Type()))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrNonStringAttrs, strings.Join(bad, ", "))
	}
	return nil
}

func firstString(m pcommon.Map, keys ...string) string {
	for _, k := range keys {
		if v, ok := attrString(m, k); ok {
			return truncateLabel(v)
		}
	}
	return ""
}

// attrInt tolerates exporters that send numbers as doubles or strings.
// Non-finite doubles (NaN/Inf), negatives, and values above maxTokensPerCall
// are rejected as missing so one crafted span cannot poison aggregates
// (SUM overflow, MinInt64 coercion) or deflate ledgers.
func attrInt(m pcommon.Map, key string) (int64, bool) {
	v, ok := m.Get(key)
	if !ok {
		return 0, false
	}
	switch v.Type() {
	case pcommon.ValueTypeInt:
		n := v.Int()
		if n < 0 || n > maxTokensPerCall {
			return 0, false
		}
		return n, true
	case pcommon.ValueTypeDouble:
		d := v.Double()
		if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > float64(maxTokensPerCall) {
			return 0, false
		}
		return int64(d), true
	case pcommon.ValueTypeStr:
		n, err := strconv.ParseInt(v.Str(), 10, 64)
		if err != nil || n < 0 || n > maxTokensPerCall {
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
		d := v.Double()
		if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > maxCostUSD {
			return 0, false
		}
		return d, true
	case pcommon.ValueTypeInt:
		n := v.Int()
		if n < 0 || float64(n) > maxCostUSD {
			return 0, false
		}
		return float64(n), true
	case pcommon.ValueTypeStr:
		f, err := strconv.ParseFloat(v.Str(), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > maxCostUSD {
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
