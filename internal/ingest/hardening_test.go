package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const snapshotColumns = `
	id, timestamp, source, service_name, provider, model,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
	cost, conversation_id, trace_id, span_id, duration_ms,
	agent_name, git_repo, git_branch`

// fixtureTraces decodes a trace fixture regardless of its on-disk encoding.
func fixtureTraces(t *testing.T, file string) ptrace.Traces {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	td, jsonErr := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	if jsonErr == nil {
		return td
	}
	td, err = (&ptrace.ProtoUnmarshaler{}).UnmarshalTraces(data)
	if err != nil {
		t.Fatalf("decode %s: json: %v / proto: %v", file, jsonErr, err)
	}
	return td
}

// rowsSnapshot dumps every generation row in a canonical, sorted form so
// different transports can be compared byte-for-byte.
func rowsSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT ` + snapshotColumns + ` FROM generations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals := make([]any, 19)
		ptrs := make([]any, 19)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(vals...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func post(t *testing.T, url, ctype string, body []byte) int {
	t.Helper()
	resp, err := http.Post(url, ctype, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Item 1: POST the same fixture batch 3× back-to-back → exactly one row set.
func TestSameBatchThreeTimesBackToBack(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	file := "../../testdata/copilot/traces-chat-simple.json"
	perSource := map[string]int{}
	for _, source := range expectedGenerationSpans(t, file) {
		perSource[source]++
	}

	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
			t.Fatalf("POST %d: status = %d, want 200", i+1, status)
		}
	}
	assertRowCounts(t, db, perSource)
}

// Item 2: every trace fixture through HTTP/JSON, HTTP/protobuf, and gRPC
// must produce byte-identical Generation rows.
func TestEncodingTransportMatrix(t *testing.T) {
	snapshots := make(map[string][]string, 3)
	for _, transport := range []string{"http-json", "http-protobuf", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			db, pipeline, srv := startIngestStack(t)
			defer db.Close()
			defer srv.Close()

			var traceClient coltracepb.TraceServiceClient
			if transport == "grpc" {
				gsrv := NewGRPCServer(pipeline, nil)
				ln, err := ServeGRPC(gsrv, "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer gsrv.Stop()
				conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				traceClient = coltracepb.NewTraceServiceClient(conn)
			}

			for _, tf := range traceFixtures {
				td := fixtureTraces(t, tf.file)
				switch transport {
				case "http-json":
					body, err := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
					if err != nil {
						t.Fatal(err)
					}
					if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
						t.Fatalf("POST %s: status = %d, want 200", tf.file, status)
					}
				case "http-protobuf":
					body, err := (&ptrace.ProtoMarshaler{}).MarshalTraces(td)
					if err != nil {
						t.Fatal(err)
					}
					if status := post(t, srv.URL+"/v1/traces", "application/x-protobuf", body); status != http.StatusOK {
						t.Fatalf("POST %s: status = %d, want 200", tf.file, status)
					}
				case "grpc":
					b, err := ptraceotlp.NewExportRequestFromTraces(td).MarshalProto()
					if err != nil {
						t.Fatal(err)
					}
					req := &coltracepb.ExportTraceServiceRequest{}
					if err := proto.Unmarshal(b, req); err != nil {
						t.Fatal(err)
					}
					if _, err := traceClient.Export(context.Background(), req); err != nil {
						t.Fatalf("grpc export %s: %v", tf.file, err)
					}
				}
			}
			snapshots[transport] = rowsSnapshot(t, db)
		})
	}
	baseline, ok := snapshots["http-json"]
	if !ok || len(baseline) == 0 {
		t.Fatal("http-json baseline snapshot empty")
	}
	for transport, snap := range snapshots {
		if transport == "http-json" {
			continue
		}
		if len(snap) != len(baseline) {
			t.Errorf("%s produced %d rows, http-json produced %d", transport, len(snap), len(baseline))
			continue
		}
		for i := range baseline {
			if snap[i] != baseline[i] {
				t.Errorf("%s row %d diverges from http-json normalization bug:\n got: %s\nwant: %s",
					transport, i, snap[i], baseline[i])
			}
		}
	}
}

// Item 2: content-type edge cases per the OTLP spec.
func TestContentTypeEdgeCases(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	body, err := os.ReadFile("../../testdata/copilot/traces-chat-simple.json")
	if err != nil {
		t.Fatal(err)
	}

	// missing Content-Type header → 400
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing content type: status = %d, want 400", resp.StatusCode)
	}

	// gzip Content-Encoding (OTLP/HTTP allows it) → 200, rows stored
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(gz.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("gzipped body: status = %d, want 200", resp.StatusCode)
	}
	perSource := map[string]int{}
	for _, source := range expectedGenerationSpans(t, "../../testdata/copilot/traces-chat-simple.json") {
		perSource[source]++
	}
	assertRowCounts(t, db, perSource)

	// corrupt gzip body → 400
	if status := post(t, srv.URL+"/v1/traces", "application/json", []byte{0x1f, 0x8b, 0xff, 0xff}); status != http.StatusBadRequest {
		t.Errorf("corrupt gzip: status = %d, want 400", status)
	}

	// unsupported Content-Encoding → 415
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("zstd encoding: status = %d, want 415", resp.StatusCode)
	}
}

// Oversized request bodies must be rejected with 413, both raw and via the
// gzip decompression path (decompression bomb).
func TestRequestBodySizeLimit(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	huge := bytes.Repeat([]byte("a"), maxBodyBytes+1)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", resp.StatusCode)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(bytes.Repeat([]byte("a"), maxBodyBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(gz.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("decompression bomb: status = %d, want 413", resp.StatusCode)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("generations rows = %d, want 0 (rejected batches must store nothing)", rows)
	}
}

// Item 3: an agent turn (invoke_agent + N chat spans) yields exactly N
// generation rows, never N+1.
func TestAgentTurnYieldsExactlyChatSpans(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	files := []string{
		"../../testdata/copilot/traces-invoke-agent.json",
		"../../testdata/copilot/traces-chat-tools.json",
	}
	var chats int
	for _, file := range files {
		for _, span := range allSpans(t, fixtureTraces(t, file)) {
			if opName(span) == "chat" {
				chats++
			}
		}
		if status := post(t, srv.URL+"/v1/traces", "application/json", mustMarshalFixture(t, file)); status != http.StatusOK {
			t.Fatalf("POST %s: unexpected status", file)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != chats {
		t.Errorf("rows = %d, want exactly %d (one per chat span, invoke_agent excluded)", rows, chats)
	}
}

// Item 3: an opencode metrics batch yields zero generations but nonzero
// received counters; logs likewise.
func TestNonTruthSignalsYieldZeroRows(t *testing.T) {
	db, pipeline, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	for _, f := range []struct{ file, path string }{
		{"../../testdata/opencode/metrics.json", "/v1/metrics"},
		{"../../testdata/opencode/logs.json", "/v1/logs"},
		{"../../testdata/copilot/metrics.json", "/v1/metrics"},
		{"../../testdata/copilot/logs.json", "/v1/logs"},
	} {
		body, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		if status := post(t, srv.URL+f.path, "application/json", body); status != http.StatusOK {
			t.Errorf("POST %s: status = %d, want 200", f.file, status)
		}
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("rows = %d, want 0 (metrics/logs never become generations)", rows)
	}
	s := pipeline.Stats()
	if s.Received == 0 {
		t.Error("received = 0, want nonzero for ingested non-truth signals")
	}
	if s.Rejected != s.Received {
		t.Errorf("rejected = %d, want == received %d (all non-truth records)", s.Rejected, s.Received)
	}
	if s.Normalized != 0 {
		t.Errorf("normalized = %d, want 0", s.Normalized)
	}
}

// Item 4: one corrupt span per batch (bad types, missing trace id, zero
// span id) — batch succeeds, the corrupt record counts normalization_errors,
// the valid record is stored.
func TestCorruptSpanVariants(t *testing.T) {
	valid := func() ptrace.Span {
		return syntheticCopilotChat("tv", "sv", map[string]any{
			"gen_ai.operation.name":     "chat",
			"gen_ai.usage.input_tokens": int64(10),
		})
	}
	variants := []struct {
		name    string
		corrupt ptrace.Span
	}{
		{"int where string expected", syntheticCopilotChat("tc", "sc", map[string]any{
			"gen_ai.operation.name":     "chat",
			"gen_ai.request.model":      int64(42),
			"gen_ai.usage.input_tokens": int64(20),
		})},
		{"missing trace id", syntheticCopilotChat("", "sc", map[string]any{
			"gen_ai.operation.name": "chat",
		})},
		{"zero span id", syntheticCopilotChat("tc", "", map[string]any{
			"gen_ai.operation.name": "chat",
		})},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			db, pipeline, srv := startIngestStack(t)
			defer db.Close()
			defer srv.Close()

			td := ptrace.NewTraces()
			rss := td.ResourceSpans().AppendEmpty()
			rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
			sss := rss.ScopeSpans().AppendEmpty()
			valid().CopyTo(sss.Spans().AppendEmpty())
			v.corrupt.CopyTo(sss.Spans().AppendEmpty())

			body, err := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
			if err != nil {
				t.Fatal(err)
			}
			if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (one corrupt span must not fail the batch)", status)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("rows = %d, want 1", count)
			}
			s := pipeline.Stats()
			if s.NormalizationErrors != 1 {
				t.Errorf("normalization_errors = %d, want 1", s.NormalizationErrors)
			}
			if s.Stored != 1 {
				t.Errorf("stored = %d, want 1", s.Stored)
			}
		})
	}
}

// Item 4: a fully undecodable body → 400, the receiver keeps running, and a
// follow-up good batch still succeeds.
func TestUndecodableBodyThenGoodBatch(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	if status := post(t, srv.URL+"/v1/traces", "application/json", []byte("not json at all")); status != http.StatusBadRequest {
		t.Fatalf("garbage body: status = %d, want 400", status)
	}
	body, err := os.ReadFile("../../testdata/opencode/traces-llm.json")
	if err != nil {
		t.Fatal(err)
	}
	if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
		t.Fatalf("follow-up good batch: status = %d, want 200", status)
	}
	perSource := map[string]int{}
	for _, source := range expectedGenerationSpans(t, "../../testdata/opencode/traces-llm.json") {
		perSource[source]++
	}
	assertRowCounts(t, db, perSource)
}

// Item 4: empty batch, empty resource spans, and a span with no attributes
// at all — no panics, correct counters.
func TestEmptyAndSparseBatches(t *testing.T) {
	db, pipeline, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	// entirely empty traces batch
	empty := ptrace.NewTraces()
	body, err := (&ptrace.JSONMarshaler{}).MarshalTraces(empty)
	if err != nil {
		t.Fatal(err)
	}
	if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
		t.Errorf("empty batch: status = %d, want 200", status)
	}

	// resource spans with no scope spans
	emptyRS := ptrace.NewTraces()
	rss := emptyRS.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
	body, err = (&ptrace.JSONMarshaler{}).MarshalTraces(emptyRS)
	if err != nil {
		t.Fatal(err)
	}
	if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
		t.Errorf("empty resource spans: status = %d, want 200", status)
	}

	// span with no attributes at all: detected via resource, rejected as
	// not a generation record
	noAttrs := ptrace.NewTraces()
	rss = noAttrs.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
	sss := rss.ScopeSpans().AppendEmpty()
	syntheticCopilotChat("tn", "sn", nil).CopyTo(sss.Spans().AppendEmpty())
	body, err = (&ptrace.JSONMarshaler{}).MarshalTraces(noAttrs)
	if err != nil {
		t.Fatal(err)
	}
	if status := post(t, srv.URL+"/v1/traces", "application/json", body); status != http.StatusOK {
		t.Errorf("span with no attributes: status = %d, want 200", status)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
	s := pipeline.Stats()
	if s.Received != 1 || s.Rejected != 1 || s.Normalized != 0 {
		t.Errorf("stats = %+v, want received=1 rejected=1 normalized=0", s)
	}
}

// Item 6: a 5,000-span batch (mostly non-chat) + 500 chat spans completes
// well within a generous timeout; rows = 500.
func TestLargeBatch(t *testing.T) {
	db, _, srv := startIngestStack(t)
	defer db.Close()
	defer srv.Close()

	td := ptrace.NewTraces()
	rss := td.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
	sss := rss.ScopeSpans().AppendEmpty()
	for i := 0; i < 5500; i++ {
		span := sss.Spans().AppendEmpty()
		var tid pcommon.TraceID
		var sid pcommon.SpanID
		copy(tid[:], fmt.Sprintf("trace-%08d", i))
		copy(sid[:], fmt.Sprintf("span-%08d", i))
		span.SetTraceID(tid)
		span.SetSpanID(sid)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(int64(1000 + i))))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(int64(1500 + i))))
		if i < 500 {
			span.Attributes().PutStr("gen_ai.operation.name", "chat")
			span.Attributes().PutStr("gen_ai.request.model", "gpt-4.1")
			span.Attributes().PutInt("gen_ai.usage.input_tokens", int64(10+i))
			span.Attributes().PutInt("gen_ai.usage.output_tokens", int64(5+i))
		} else {
			span.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		}
	}
	body, err := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		done <- post(t, srv.URL+"/v1/traces", "application/json", body)
	}()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("large batch did not complete within 10s")
	}

	var chats int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations WHERE source = 'copilot'`).Scan(&chats); err != nil {
		t.Fatal(err)
	}
	if chats != 500 {
		t.Errorf("rows = %d, want 500 (invoke_agent spans excluded)", chats)
	}
}

func mustMarshalFixture(t *testing.T, file string) []byte {
	t.Helper()
	td := fixtureTraces(t, file)
	body, err := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
