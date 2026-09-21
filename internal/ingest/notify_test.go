package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/storage"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func expectSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hub signal")
	}
}

func expectNoSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected hub signal")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestPipelineNotifiesHubOnStoredBatch: a batch that stores new
// generations notifies the hub once; a fully deduplicated batch stays
// silent, so the dashboard never refreshes for retried exports — with or
// without a pricing wake wired. The pricing wake itself follows the
// batch's records, not the stored count: batches whose records all carry
// harness costs never wake the worker, while unpriced records do.
func TestPipelineNotifiesHubOnStoredBatch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}

	hub := live.New()
	ch, cancel := hub.Subscribe()
	defer cancel()
	pipeline := NewPipeline(db, nil, hub)
	wakes := 0
	pipeline.AfterCommit = func() { wakes++ }

	data, err := os.ReadFile("../../testdata/opencode/traces-llm.json")
	if err != nil {
		t.Fatal(err)
	}
	td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	if err != nil {
		t.Fatal(err)
	}

	// OpenCode reports a cost for every generation: nothing is pending, so
	// the hub fires but the pricing worker never wakes.
	if err := pipeline.ConsumeTraces(ctx, td); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	expectSignal(t, ch)
	if wakes != 0 {
		t.Fatalf("costed batch woke the pricing worker %d times", wakes)
	}

	// Retried export deduplicates: no new rows, no signal, no wake.
	if err := pipeline.ConsumeTraces(ctx, td); err != nil {
		t.Fatalf("retried consume: %v", err)
	}
	expectNoSignal(t, ch)
	if wakes != 0 {
		t.Fatalf("costed retry woke the pricing worker %d times", wakes)
	}

	// Copilot reports no costs, so every record queues pricing work.
	copilot, err := os.ReadFile("../../testdata/copilot/traces-chat-simple.json")
	if err != nil {
		t.Fatal(err)
	}
	ctd, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(copilot)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ConsumeTraces(ctx, ctd); err != nil {
		t.Fatalf("copilot consume: %v", err)
	}
	expectSignal(t, ch)
	if wakes != 1 {
		t.Fatalf("unpriced batch must wake the pricing worker once, got %d", wakes)
	}
	// Its retry stays silent for the dashboard but still pings the worker:
	// a merged row with newly filled tokens is re-queued, and the wake is
	// coalesced, so the cheap check replaces tracking merge outcomes.
	if err := pipeline.ConsumeTraces(ctx, ctd); err != nil {
		t.Fatalf("copilot retry: %v", err)
	}
	expectNoSignal(t, ch)
	if wakes != 2 {
		t.Fatalf("unpriced retry must still wake the pricing worker, got %d", wakes)
	}
}
