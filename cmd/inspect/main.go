package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func printAttrs(m pcommon.Map, indent string) {
	m.Range(func(k string, v pcommon.Value) bool {
		fmt.Printf("%s%s: %s = %s\n", indent, k, v.Type(), v.AsRaw())
		return true
	})
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: inspect <traces|metrics|logs> <file>")
		os.Exit(2)
	}
	signal, path := os.Args[1], os.Args[2]
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	switch signal {
	case "traces":
		td, err := decodeTraces(data)
		if err != nil {
			panic(err)
		}
		for i, rs := range td.ResourceSpans().All() {
			fmt.Printf("== resourceSpans[%d]\n", i)
			fmt.Println("-- resource attributes:")
			printAttrs(rs.Resource().Attributes(), "  ")
			for j, scope := range rs.ScopeSpans().All() {
				fmt.Printf("-- scope[%d]: %s %s\n", j, scope.Scope().Name(), scope.Scope().Version())
				for k, span := range scope.Spans().All() {
					fmt.Printf("  == span[%d] name=%q kind=%s traceID=%s spanID=%s parent=%s\n",
						k, span.Name(), span.Kind(), span.TraceID(), span.SpanID(), span.ParentSpanID())
					fmt.Printf("     start=%s end=%s dur=%s status=%v\n",
						span.StartTimestamp().AsTime(), span.EndTimestamp().AsTime(),
						span.EndTimestamp().AsTime().Sub(span.StartTimestamp().AsTime()), span.Status().Code())
					printAttrs(span.Attributes(), "     ")
					for e := 0; e < span.Events().Len(); e++ {
						ev := span.Events().At(e)
						fmt.Printf("     event[%d] %q @%s\n", e, ev.Name(), ev.Timestamp().AsTime())
						printAttrs(ev.Attributes(), "       ")
					}
				}
			}
		}
	case "metrics":
		md, err := decodeMetrics(data)
		if err != nil {
			panic(err)
		}
		for i, rm := range md.ResourceMetrics().All() {
			fmt.Printf("== resourceMetrics[%d]\n", i)
			fmt.Println("-- resource attributes:")
			printAttrs(rm.Resource().Attributes(), "  ")
			for j, sm := range rm.ScopeMetrics().All() {
				fmt.Printf("-- scope[%d]: %s %s\n", j, sm.Scope().Name(), sm.Scope().Version())
				for k, m := range sm.Metrics().All() {
					fmt.Printf("  == metric[%d] name=%q desc=%q unit=%q type=", k, m.Name(), m.Description(), m.Unit())
					switch m.Type() {
					case pmetric.MetricTypeSum:
						fmt.Printf("sum(aggregation=%s, monotonic=%v)\n", m.Sum().AggregationTemporality(), m.Sum().IsMonotonic())
						for d := 0; d < m.Sum().DataPoints().Len(); d++ {
							dp := m.Sum().DataPoints().At(d)
							fmt.Printf("     dp[%d] value=%v start=%s time=%s\n", d, value(dp), dp.StartTimestamp().AsTime(), dp.Timestamp().AsTime())
							printAttrs(dp.Attributes(), "       ")
						}
					case pmetric.MetricTypeGauge:
						fmt.Println("gauge")
						for d := 0; d < m.Gauge().DataPoints().Len(); d++ {
							dp := m.Gauge().DataPoints().At(d)
							fmt.Printf("     dp[%d] value=%v time=%s\n", d, value(dp), dp.Timestamp().AsTime())
							printAttrs(dp.Attributes(), "       ")
						}
					case pmetric.MetricTypeHistogram:
						fmt.Printf("histogram(count=%d sum=%v)\n", m.Histogram().DataPoints().Len(), 0)
						for d := 0; d < m.Histogram().DataPoints().Len(); d++ {
							dp := m.Histogram().DataPoints().At(d)
							fmt.Printf("     dp[%d] count=%d sum=%v min=%v max=%v time=%s\n", d, dp.Count(), dp.Sum(), dp.Min(), dp.Max(), dp.Timestamp().AsTime())
							printAttrs(dp.Attributes(), "       ")
						}
					default:
						fmt.Println(m.Type())
					}
				}
			}
		}
	case "logs":
		ld, err := decodeLogs(data)
		if err != nil {
			panic(err)
		}
		for i, rl := range ld.ResourceLogs().All() {
			fmt.Printf("== resourceLogs[%d]\n", i)
			fmt.Println("-- resource attributes:")
			printAttrs(rl.Resource().Attributes(), "  ")
			for j, sl := range rl.ScopeLogs().All() {
				fmt.Printf("-- scope[%d]: %s %s\n", j, sl.Scope().Name(), sl.Scope().Version())
				for k, lr := range sl.LogRecords().All() {
					fmt.Printf("  == log[%d] severity=%q @%s observed=%s\n", k, lr.SeverityText(), lr.Timestamp().AsTime(), lr.ObservedTimestamp().AsTime())
					fmt.Printf("     body=%s\n", bodyJSON(lr))
					fmt.Printf("     traceID=%s spanID=%s\n", lr.TraceID(), lr.SpanID())
					printAttrs(lr.Attributes(), "     ")
				}
			}
		}
	default:
		panic("signal must be traces|metrics|logs")
	}
}

func bodyJSON(lr plog.LogRecord) string {
	if lr.Body().Type() == pcommon.ValueTypeEmpty {
		return "<empty>"
	}
	b, err := json.Marshal(lr.Body().AsRaw())
	if err != nil {
		return fmt.Sprintf("%v", lr.Body().AsRaw())
	}
	return strings.TrimSpace(string(b))
}

func decodeTraces(data []byte) (ptrace.Traces, error) {
	if looksJSON(data) {
		u := &ptrace.JSONUnmarshaler{}
		return u.UnmarshalTraces(data)
	}
	u := ptrace.ProtoUnmarshaler{}
	return u.UnmarshalTraces(data)
}

func decodeMetrics(data []byte) (pmetric.Metrics, error) {
	if looksJSON(data) {
		u := &pmetric.JSONUnmarshaler{}
		return u.UnmarshalMetrics(data)
	}
	u := pmetric.ProtoUnmarshaler{}
	return u.UnmarshalMetrics(data)
}

func decodeLogs(data []byte) (plog.Logs, error) {
	if looksJSON(data) {
		u := &plog.JSONUnmarshaler{}
		return u.UnmarshalLogs(data)
	}
	u := plog.ProtoUnmarshaler{}
	return u.UnmarshalLogs(data)
}

func looksJSON(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func value(dp pmetric.NumberDataPoint) any {
	switch dp.ValueType() {
	case pmetric.NumberDataPointValueTypeInt:
		return dp.IntValue()
	default:
		return dp.DoubleValue()
	}
}
