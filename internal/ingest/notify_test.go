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
// silent, so the dashboard never refreshes for retried exports.
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

	data, err := os.ReadFile("../../testdata/opencode/traces-llm.json")
	if err != nil {
		t.Fatal(err)
	}
	td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	if err != nil {
		t.Fatal(err)
	}

	if err := pipeline.ConsumeTraces(ctx, td); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	expectSignal(t, ch)

	// Retried export deduplicates: no new rows, no signal.
	if err := pipeline.ConsumeTraces(ctx, td); err != nil {
		t.Fatalf("retried consume: %v", err)
	}
	expectNoSignal(t, ch)
}
