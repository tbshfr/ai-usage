package sanitizeotel

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestLogsRedactsAllDuplicateSensitiveAttributes(t *testing.T) {
	raw := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"host.name","value":{"stringValue":"secret-host"}}]},"scopeLogs":[{"logRecords":[{"attributes":[{"key":"event.name","value":{"stringValue":"codex.user_prompt"}},{"key":"thread.id","value":{"stringValue":"secret-one"}},{"key":"thread.id","value":{"stringValue":"secret-two"}},{"key":"prompt","value":{"stringValue":"secret prompt"}}]}]}]}]}`)
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(raw)
	if err != nil {
		t.Fatal(err)
	}
	Logs(logs)
	encoded, err := (&plog.JSONMarshaler{}).MarshalLogs(logs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"secret-host", "secret-one", "secret-two", "secret prompt"} {
		if strings.Contains(text, secret) {
			t.Errorf("sanitized output still contains %q", secret)
		}
	}
	if strings.Count(text, Redacted) < 3 {
		t.Errorf("sanitized output = %s", text)
	}
}

func TestTracesRedactsStatusAndAttributes(t *testing.T) {
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.Attributes().PutStr("code.file.path", "/secret/path")
	span.Status().SetMessage("secret error")
	Traces(traces)
	if got, _ := span.Attributes().Get("code.file.path"); got.Str() != Redacted {
		t.Errorf("code.file.path = %q", got.Str())
	}
	if got := span.Status().Message(); got != Redacted {
		t.Errorf("status = %q", got)
	}
}

func TestMakiLogsRedactOptInContent(t *testing.T) {
	logs := plog.NewLogs()
	record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Attributes().PutStr("event.name", "maki.tool_result")
	record.Attributes().PutStr("tool_input", "secret command")
	record.Attributes().PutStr("error", "secret path")
	record.Body().SetStr("secret body")
	Logs(logs)
	encoded, err := (&plog.JSONMarshaler{}).MarshalLogs(logs)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret command", "secret path", "secret body"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("sanitized Maki log still contains %q", secret)
		}
	}
	for _, key := range []string{"tool_input", "error"} {
		v, ok := record.Attributes().Get(key)
		if !ok || v.Str() != Redacted {
			t.Errorf("%s was not redacted in place", key)
		}
	}
}

func TestOtherLogsKeepErrorButRedactToolInput(t *testing.T) {
	logs := plog.NewLogs()
	record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Attributes().PutStr("event.name", "other.event")
	record.Attributes().PutStr("error", "diagnostic")
	record.Attributes().PutStr("tool_input", "secret command")
	Logs(logs)
	v, ok := record.Attributes().Get("error")
	if !ok || v.Str() != "diagnostic" {
		t.Errorf("unrelated error attribute = %v", v)
	}
	v, ok = record.Attributes().Get("tool_input")
	if !ok || v.Str() != Redacted {
		t.Errorf("unrelated tool_input attribute = %v", v)
	}
}
