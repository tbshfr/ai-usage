// Package sanitizeotel removes content and identity fields from decoded OTLP
// payloads while preserving the signal structure and numeric telemetry used by
// tests and capture inspection.
package sanitizeotel

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const Redacted = "[REDACTED]"

var sensitiveKeys = map[string]struct{}{
	"arguments":                      {},
	"call_id":                        {},
	"code.file.path":                 {},
	"content":                        {},
	"conversation.id":                {},
	"copilot_chat.reasoning_content": {},
	"copilot_chat.user_request":      {},
	"cwd":                            {},
	"exception.message":              {},
	"exception.stacktrace":           {},
	"gen_ai.input.messages":          {},
	"gen_ai.output.messages":         {},
	"gen_ai.system_instructions":     {},
	"gen_ai.tool.call.arguments":     {},
	"gen_ai.tool.call.result":        {},
	"gen_ai.tool.definitions":        {},
	"host.name":                      {},
	"input.value":                    {},
	"llm.input_messages":             {},
	"llm.output_messages":            {},
	"output":                         {},
	"output.value":                   {},
	"prompt":                         {},
	"session.id":                     {},
	"thread.id":                      {},
	"turn.id":                        {},
	"user.account_id":                {},
	"user.email":                     {},
}

func attributes(attrs pcommon.Map) {
	keys := map[string]struct{}{}
	attrs.Range(func(key string, _ pcommon.Value) bool {
		if _, sensitive := sensitiveKeys[key]; sensitive {
			keys[key] = struct{}{}
		}
		return true
	})
	for key := range keys {
		attrs.Remove(key)
		attrs.PutStr(key, Redacted)
	}
}

// Traces sanitizes a decoded trace payload in place.
func Traces(td ptrace.Traces) {
	for _, rs := range td.ResourceSpans().All() {
		attributes(rs.Resource().Attributes())
		for _, ss := range rs.ScopeSpans().All() {
			attributes(ss.Scope().Attributes())
			for _, span := range ss.Spans().All() {
				attributes(span.Attributes())
				for _, event := range span.Events().All() {
					attributes(event.Attributes())
				}
				for _, link := range span.Links().All() {
					attributes(link.Attributes())
				}
				if span.Status().Message() != "" {
					span.Status().SetMessage(Redacted)
				}
			}
		}
	}
}

// Logs sanitizes a decoded log payload in place.
func Logs(ld plog.Logs) {
	for _, rl := range ld.ResourceLogs().All() {
		attributes(rl.Resource().Attributes())
		for _, sl := range rl.ScopeLogs().All() {
			attributes(sl.Scope().Attributes())
			for _, record := range sl.LogRecords().All() {
				name := ""
				if value, ok := record.Attributes().Get("event.name"); ok && value.Type() == pcommon.ValueTypeStr {
					name = value.Str()
				}
				attributes(record.Attributes())
				if (name == "codex.user_prompt" || name == "codex.tool_result") && record.Body().Type() != pcommon.ValueTypeEmpty {
					record.Body().SetStr(Redacted)
				}
			}
		}
	}
}

// Metrics sanitizes a decoded metric payload in place.
func Metrics(md pmetric.Metrics) {
	for _, rm := range md.ResourceMetrics().All() {
		attributes(rm.Resource().Attributes())
		for _, sm := range rm.ScopeMetrics().All() {
			attributes(sm.Scope().Attributes())
			for _, metric := range sm.Metrics().All() {
				switch metric.Type() {
				case pmetric.MetricTypeGauge:
					for _, point := range metric.Gauge().DataPoints().All() {
						numberPoint(point)
					}
				case pmetric.MetricTypeSum:
					for _, point := range metric.Sum().DataPoints().All() {
						numberPoint(point)
					}
				case pmetric.MetricTypeHistogram:
					for _, point := range metric.Histogram().DataPoints().All() {
						attributes(point.Attributes())
						for _, exemplar := range point.Exemplars().All() {
							attributes(exemplar.FilteredAttributes())
						}
					}
				case pmetric.MetricTypeExponentialHistogram:
					for _, point := range metric.ExponentialHistogram().DataPoints().All() {
						attributes(point.Attributes())
						for _, exemplar := range point.Exemplars().All() {
							attributes(exemplar.FilteredAttributes())
						}
					}
				case pmetric.MetricTypeSummary:
					for _, point := range metric.Summary().DataPoints().All() {
						attributes(point.Attributes())
					}
				}
			}
		}
	}
}

func numberPoint(point pmetric.NumberDataPoint) {
	attributes(point.Attributes())
	for _, exemplar := range point.Exemplars().All() {
		attributes(exemplar.FilteredAttributes())
	}
}
