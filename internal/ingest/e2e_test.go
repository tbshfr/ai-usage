package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/normalize"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

var traceFixtures = []struct{ file, ctype string }{
	{"../../testdata/copilot/traces-chat-simple.json", "application/json"},
	{"../../testdata/copilot/traces-chat-tools.json", "application/json"},
	{"../../testdata/copilot/traces-legacy-reasoning.json", "application/json"},
	{"../../testdata/copilot/traces-invoke-agent.json", "application/json"},
	{"../../testdata/opencode/traces-llm.json", "application/json"},
	{"../../testdata/opencode/traces-llm.pb", "application/x-protobuf"},
	{"../../testdata/opencode/traces-llm-nocache.json", "application/json"},
	{"../../testdata/opencode/traces-llm-multiturn.json", "application/json"},
}

// expectedGenerationSpans walks the fixtures with pdata and counts the
// spans the normalizers must accept, per source, keyed by trace/span ID.
func expectedGenerationSpans(t *testing.T, file string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	u := &ptrace.JSONUnmarshaler{}
	td, err := u.UnmarshalTraces(data)
	if err != nil {
		pu := ptrace.ProtoUnmarshaler{}
		td, err = pu.UnmarshalTraces(data)
		if err != nil {
			t.Fatalf("decode %s: %v", file, err)
		}
	}
	out := map[string]string{}
	for _, rs := range td.ResourceSpans().All() {
		resource := rs.Resource().Attributes()
		for _, ss := range rs.ScopeSpans().All() {
			for _, span := range ss.Spans().All() {
				source := normalize.DetectSource(resource, span.Attributes())
				switch source {
				case "copilot":
					if op, _ := span.Attributes().Get("gen_ai.operation.name"); op.Str() == "chat" {
						out[span.TraceID().String()+"|"+span.SpanID().String()] = source
					}
				case "opencode":
					kind, _ := span.Attributes().Get("openinference.span.kind")
					if kind.Str() == "LLM" || span.Name() == "opencode.llm" {
						out[span.TraceID().String()+"|"+span.SpanID().String()] = source
					}
				}
			}
		}
	}
	return out
}

func TestEndToEndHTTP(t *testing.T) {
	db, pipeline, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	client := srv.Client()

	post := func(file, ctype string) {
		t.Helper()
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Post(srv.URL+"/v1/traces", ctype, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST %s: status = %d, want 200", file, resp.StatusCode)
		}
	}

	// expected unique generation records across all trace fixtures
	expected := map[string]string{}
	expectedPerSource := map[string]int{}
	for _, tf := range traceFixtures {
		for key, source := range expectedGenerationSpans(t, tf.file) {
			if _, seen := expected[key]; !seen {
				expected[key] = source
				expectedPerSource[source]++
			}
		}
	}
	if expectedPerSource["copilot"] == 0 || expectedPerSource["opencode"] == 0 {
		t.Fatalf("fixture expectations broken: %v", expectedPerSource)
	}

	// first pass: everything stored exactly once
	for _, tf := range traceFixtures {
		post(tf.file, tf.ctype)
	}
	assertRowCounts(t, db, expectedPerSource)

	// non-trace signals must never produce rows
	for _, f := range []struct{ file, path, ctype string }{
		{"../../testdata/copilot/metrics.json", "/v1/metrics", "application/json"},
		{"../../testdata/opencode/metrics.json", "/v1/metrics", "application/json"},
		{"../../testdata/copilot/logs.json", "/v1/logs", "application/json"},
		{"../../testdata/opencode/logs.json", "/v1/logs", "application/json"},
	} {
		body, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Post(srv.URL+f.path, f.ctype, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST %s: status = %d, want 200", f.file, resp.StatusCode)
		}
	}
	assertRowCounts(t, db, expectedPerSource)

	// second pass: retried batches deduplicate
	before := pipeline.Stats()
	for _, tf := range traceFixtures {
		post(tf.file, tf.ctype)
	}
	assertRowCounts(t, db, expectedPerSource)
	after := pipeline.Stats()
	if after.Deduplicated-before.Deduplicated < uint64(len(expected)) {
		t.Errorf("second pass dedup = %d, want >= %d", after.Deduplicated-before.Deduplicated, len(expected))
	}
	if after.Stored != before.Stored {
		t.Errorf("second pass stored %d new rows, want 0", after.Stored-before.Stored)
	}

	// stats invariant
	s := pipeline.Stats()
	if s.Received != s.Normalized+s.Rejected+s.IgnoredNotUsed+s.NormalizationErrors {
		t.Errorf("received %d != normalized %d + rejected %d + ignored %d + norm_errors %d",
			s.Received, s.Normalized, s.Rejected, s.IgnoredNotUsed, s.NormalizationErrors)
	}
	if s.Normalized != s.Stored+s.Deduplicated {
		t.Errorf("normalized %d != stored %d + deduplicated %d",
			s.Normalized, s.Stored, s.Deduplicated)
	}
	if s.IngestionErrors != 0 {
		t.Errorf("ingestion errors = %d, want 0", s.IngestionErrors)
	}
}

func TestMalformedSpanDoesNotFailBatch(t *testing.T) {
	db, pipeline, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	// one valid copilot chat span + one with no IDs (undeducible → error)
	valid := syntheticCopilotChat("t-valid", "s-valid", map[string]any{
		"gen_ai.operation.name":     "chat",
		"gen_ai.usage.input_tokens": int64(10),
	})
	broken := syntheticCopilotChat("", "", map[string]any{
		"gen_ai.operation.name":     "chat",
		"gen_ai.usage.input_tokens": int64(20),
	})
	td := ptrace.NewTraces()
	rss := td.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
	sss := rss.ScopeSpans().AppendEmpty()
	valid.CopyTo(sss.Spans().AppendEmpty())
	broken.CopyTo(sss.Spans().AppendEmpty())

	m := &ptrace.JSONMarshaler{}
	body, err := m.MarshalTraces(td)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/v1/traces", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch with one malformed span: status = %d, want 200", resp.StatusCode)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1 (valid record stored, malformed skipped)", count)
	}
	s := pipeline.Stats()
	if s.NormalizationErrors != 1 {
		t.Errorf("normalization_errors = %d, want 1", s.NormalizationErrors)
	}
	if s.Stored != 1 {
		t.Errorf("stored = %d, want 1", s.Stored)
	}
}

// syntheticCopilotChat builds a copilot chat span; empty trace/span IDs
// simulate an undeducible (malformed) record.
func syntheticCopilotChat(traceID, spanID string, attrs map[string]any) ptrace.Span {
	span := ptrace.NewSpan()
	if traceID != "" {
		var tid pcommon.TraceID
		copy(tid[:], traceID)
		span.SetTraceID(tid)
	}
	if spanID != "" {
		var sid pcommon.SpanID
		copy(sid[:], spanID)
		span.SetSpanID(sid)
	}
	span.SetName("chat test")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(1000)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(1500)))
	if err := span.Attributes().FromRaw(attrs); err != nil {
		panic(err)
	}
	return span
}

func startIngestStack(t *testing.T) (*sql.DB, *Pipeline, *httptest.Server) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	pipeline := NewPipeline(db, nil, nil)
	srv := httptest.NewServer(NewReceiver(pipeline, nil).Handler())
	t.Cleanup(srv.Close)
	return db, pipeline, srv
}

func assertRowCounts(t *testing.T, db *sql.DB, expectedPerSource map[string]int) {
	t.Helper()
	rows, err := db.Query(`SELECT source, COUNT(*) FROM generations GROUP BY source`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var source string
		var n int
		if err := rows.Scan(&source, &n); err != nil {
			t.Fatal(err)
		}
		got[source] = n
	}
	for source, want := range expectedPerSource {
		if got[source] != want {
			t.Errorf("rows for %s = %d, want %d", source, got[source], want)
		}
	}
	total := 0
	for _, n := range got {
		total += n
	}
	wantTotal := 0
	for _, n := range expectedPerSource {
		wantTotal += n
	}
	if total != wantTotal {
		t.Errorf("total rows = %d, want %d (no unexpected sources)", total, wantTotal)
	}
}
