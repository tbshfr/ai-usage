package normalize

import (
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type namedSpan struct {
	resource pcommon.Map
	span     ptrace.Span
}

type namedLog struct {
	resource pcommon.Map
	record   plog.LogRecord
}

// syntheticSpan builds a single-span trace with the given attributes and
// service name, with real (non-zero) trace/span IDs.
func syntheticSpan(attrs map[string]any, serviceName string) namedSpan {
	td := ptrace.NewTraces()
	rss := td.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", serviceName)
	sss := rss.ScopeSpans().AppendEmpty()
	span := sss.Spans().AppendEmpty()
	span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(1000)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(2000)))
	if err := span.Attributes().FromRaw(attrs); err != nil {
		panic(err)
	}
	return namedSpan{resource: rss.Resource().Attributes(), span: span}
}

func loadLogs(t *testing.T, path string) []namedLog {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	var records []namedLog
	for _, rl := range logs.ResourceLogs().All() {
		for _, sl := range rl.ScopeLogs().All() {
			for _, record := range sl.LogRecords().All() {
				records = append(records, namedLog{resource: rl.Resource().Attributes(), record: record})
			}
		}
	}
	return records
}

func loadSpans(t *testing.T, path string) []namedSpan {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := &ptrace.JSONUnmarshaler{}
	td, err := u.UnmarshalTraces(data)
	if err != nil {
		pu := ptrace.ProtoUnmarshaler{}
		td, err = pu.UnmarshalTraces(data)
	}
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	var spans []namedSpan
	for _, rs := range td.ResourceSpans().All() {
		resource := rs.Resource().Attributes()
		for _, ss := range rs.ScopeSpans().All() {
			for _, span := range ss.Spans().All() {
				spans = append(spans, namedSpan{resource: resource, span: span})
			}
		}
	}
	return spans
}
