package ingest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// copilotTraces builds one trace batch from raw attribute maps.
func copilotTraces(spans ...ptrace.Span) ptrace.Traces {
	td := ptrace.NewTraces()
	rss := td.ResourceSpans().AppendEmpty()
	rss.Resource().Attributes().PutStr("service.name", "copilot-chat")
	sss := rss.ScopeSpans().AppendEmpty()
	for _, s := range spans {
		s.CopyTo(sss.Spans().AppendEmpty())
	}
	return td
}

func wantReason(t *testing.T, got ReasonCounts, kind, reason string, want uint64) {
	t.Helper()
	n := got[kind][reason]
	if n != want {
		t.Errorf("reasons[%s][%s] = %d, want %d (full: %v)", kind, reason, n, want, got)
	}
}

func TestReasonCountersFromPipeline(t *testing.T) {
	p := newStatsPipeline(t)
	ctx := context.Background()

	valid := func(traceID, spanID string) ptrace.Span {
		return syntheticCopilotChat(traceID, spanID, map[string]any{
			"gen_ai.operation.name":     "chat",
			"gen_ai.usage.input_tokens": int64(10),
		})
	}
	// One healthy generation, one tool call (never a generation), one
	// span without source markers, one corrupt-attribute span, one
	// span without IDs.
	td := copilotTraces(
		valid("t1", "s1"),
		syntheticCopilotChat("t2", "s2", map[string]any{
			"gen_ai.operation.name": "execute_tool",
		}),
	)
	// ...but this batch's resource makes t2/t3 copilot; build a second
	// resource-less batch for the no-source span.
	noSource := ptrace.NewTraces()
	nsSpan := syntheticCopilotChat("t4", "s4", map[string]any{"gen_ai.operation.name": "chat"})
	nsRss := noSource.ResourceSpans().AppendEmpty()
	nsRss.Resource().Attributes().PutStr("service.name", "unknown-service")
	nsSpan.CopyTo(nsRss.ScopeSpans().AppendEmpty().Spans().AppendEmpty())

	corrupt := copilotTraces(syntheticCopilotChat("t5", "s5", map[string]any{
		"gen_ai.operation.name": "chat",
		"gen_ai.request.model":  int64(42),
	}))
	noIDs := copilotTraces(syntheticCopilotChat("", "s6", map[string]any{
		"gen_ai.operation.name": "chat",
	}))

	for _, td := range []*ptrace.Traces{&td, &noSource, &corrupt, &noIDs} {
		if err := p.ConsumeTraces(ctx, *td); err != nil {
			t.Fatal(err)
		}
	}
	// Retry the first batch: the healthy record dedups by source.
	if err := p.ConsumeTraces(ctx, td); err != nil {
		t.Fatal(err)
	}

	logs := plog.NewLogs()
	logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	if err := p.ConsumeLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	metrics := pmetric.NewMetrics()
	metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty().SetEmptyGauge()
	if err := p.ConsumeMetrics(ctx, metrics); err != nil {
		t.Fatal(err)
	}

	got := p.ReasonCounts()
	wantReason(t, got, ReasonKindRejected, ReasonNoSource, 1)
	// The re-consumed batch rejects the tool call a second time and
	// dedups the healthy generation once.
	wantReason(t, got, ReasonKindRejected, ReasonNotGeneration, 2)
	wantReason(t, got, ReasonKindNormError, ReasonBadAttrs, 1)
	wantReason(t, got, ReasonKindNormError, ReasonBadIDs, 1)
	wantReason(t, got, ReasonKindDedup, "copilot", 1)
	wantReason(t, got, ReasonKindIgnored, ReasonLogs, 1)
	wantReason(t, got, ReasonKindIgnored, ReasonMetrics, 1)
	if n := got[ReasonKindDedup]["opencode"]; n != 0 {
		t.Errorf("reasons[dedup][opencode] = %d, want 0", n)
	}
	if n := got[ReasonKindHTTPReject][ReasonUnauthorized]; n != 0 {
		t.Errorf("reasons[http_reject][unauthorized] = %d, want 0", n)
	}
}

func TestBumpHTTPRejectCounts(t *testing.T) {
	p := newStatsPipeline(t)
	p.BumpHTTPReject(ReasonUnauthorized)
	p.BumpHTTPReject(ReasonUnauthorized)
	p.BumpHTTPReject(ReasonBadContentType)
	got := p.ReasonCounts()
	wantReason(t, got, ReasonKindHTTPReject, ReasonUnauthorized, 2)
	wantReason(t, got, ReasonKindHTTPReject, ReasonBadContentType, 1)
}

// TestHTTPRejectAllowlistMatchesCanonicalList guards the fixed enum: the
// allowlist map is built from HTTPRejectReasons, so the two cannot drift,
// and every canonical reason must bump (a new const missing from the slice
// is a silent drop, so keep the slice next to the consts in pipeline.go).
func TestHTTPRejectAllowlistMatchesCanonicalList(t *testing.T) {
	if len(HTTPRejectReasons) == 0 {
		t.Fatal("HTTPRejectReasons is empty")
	}
	seen := map[string]bool{}
	for _, r := range HTTPRejectReasons {
		if r == "" {
			t.Errorf("HTTPRejectReasons contains empty reason")
		}
		if seen[r] {
			t.Errorf("HTTPRejectReasons duplicates %q", r)
		}
		seen[r] = true
		if _, ok := validHTTPRejectReasons[r]; !ok {
			t.Errorf("allowlist missing canonical reason %q", r)
		}
	}
	if len(validHTTPRejectReasons) != len(seen) {
		t.Errorf("allowlist size %d != canonical list size %d", len(validHTTPRejectReasons), len(seen))
	}
	// Every allowlisted reason must actually count.
	p := newStatsPipeline(t)
	for _, r := range HTTPRejectReasons {
		p.BumpHTTPReject(r)
	}
	got := p.ReasonCounts()
	for _, r := range HTTPRejectReasons {
		wantReason(t, got, ReasonKindHTTPReject, r, 1)
	}
	// Request-derived strings must never create rows.
	p.BumpHTTPReject("application/json")
	if n := p.ReasonCounts()[ReasonKindHTTPReject]["application/json"]; n != 0 {
		t.Errorf("request-derived reason counted: %d", n)
	}
}

func TestReceiverRejectsAreCounted(t *testing.T) {
	db, pipeline, srv := startIngestStack(t)
	defer db.Close()

	// Bad content type.
	if status := post(t, srv.URL+"/v1/traces", "text/plain", []byte("{}")); status != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", status)
	}
	// Undecodable payload.
	if status := post(t, srv.URL+"/v1/traces", "application/json", []byte("{not json")); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	got := pipeline.ReasonCounts()
	wantReason(t, got, ReasonKindHTTPReject, ReasonBadContentType, 1)
	wantReason(t, got, ReasonKindHTTPReject, ReasonDecodeFailed, 1)
	// No pipeline record counters may have moved.
	if s := pipeline.Stats(); s.Received != 0 {
		t.Errorf("received = %d, want 0 (rejections never reach the pipeline)", s.Received)
	}
}

func TestReasonsSaveRestoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	p := newStatsPipeline(t)
	p.received.Add(3)
	p.BumpHTTPReject(ReasonUnauthorized)
	p.BumpHTTPReject(ReasonUnauthorized)
	p.bumpReason(ReasonKindRejected, ReasonNoSource, 5)

	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	today := utcDay(time.Now())
	rows, found, err := storage.DailyReasonsForDay(ctx, p.db, today)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted reason rows = %d, want 2: %v", len(rows), rows)
	}

	// A restarted pipeline continues today's reason counters.
	p2 := newStatsPipelineOn(t, p.db)
	if err := p2.RestoreBase(ctx); err != nil {
		t.Fatal(err)
	}
	p2.BumpHTTPReject(ReasonUnauthorized)
	got := p2.ReasonCounts()
	wantReason(t, got, ReasonKindHTTPReject, ReasonUnauthorized, 3)
	wantReason(t, got, ReasonKindRejected, ReasonNoSource, 5)

	// Save again: the absolute snapshot must not double count.
	if err := p2.Save(); err != nil {
		t.Fatal(err)
	}
	rows, _, err = storage.DailyReasonsForDay(ctx, p.db, today)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch {
		case r.Kind == ReasonKindHTTPReject && r.Reason == ReasonUnauthorized:
			if r.Count != 3 {
				t.Errorf("saved unauthorized = %d, want 3", r.Count)
			}
		case r.Kind == ReasonKindRejected && r.Reason == ReasonNoSource:
			if r.Count != 5 {
				t.Errorf("saved no_source = %d, want 5", r.Count)
			}
		default:
			t.Errorf("unexpected persisted row: %v", r)
		}
	}
}

func TestReasonsRolloverResets(t *testing.T) {
	p := newStatsPipeline(t)
	p.baseDay = "2026-08-31"
	p.baseReasons = map[reasonKey]uint64{{ReasonKindHTTPReject, ReasonUnauthorized}: 7}
	p.BumpHTTPReject(ReasonUnauthorized)

	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	old, found, err := storage.DailyReasonsForDay(ctx, p.db, "2026-08-31")
	if err != nil || !found {
		t.Fatalf("old day found=%v err=%v", found, err)
	}
	if len(old) != 1 || old[0].Count != 8 {
		t.Errorf("old day rows = %v, want unauthorized 8", old)
	}
	// The new day starts clean: today has no rows until something happens.
	if _, found, err := storage.DailyReasonsForDay(ctx, p.db, utcDay(time.Now())); err != nil || found {
		t.Errorf("new day rows exist after rollover save: found=%v err=%v", found, err)
	}
	if got := p.ReasonCounts(); len(got) != 0 {
		t.Errorf("reasons not reset: %v", got)
	}
}
