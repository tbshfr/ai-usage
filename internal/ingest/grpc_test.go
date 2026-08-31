package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/tbshfr/ai-usage/internal/storage"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

func TestGRPCTracesIngestion(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	pipeline := NewPipeline(db, nil)
	server := NewGRPCServer(pipeline, nil)

	ln, err := ServeGRPC(server, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)

	req := protoTraceRequest(t, "../../testdata/opencode/traces-llm.json")
	if _, err := client.Export(ctx, req); err != nil {
		t.Fatalf("grpc export: %v", err)
	}

	var opencodeRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations WHERE source = 'opencode'`).Scan(&opencodeRows); err != nil {
		t.Fatal(err)
	}
	if opencodeRows != 1 {
		t.Errorf("opencode rows = %d, want 1", opencodeRows)
	}

	// retried export must deduplicate
	if _, err := client.Export(ctx, req); err != nil {
		t.Fatalf("grpc re-export: %v", err)
	}
	if opencodeRows = recount(t, db, "opencode"); opencodeRows != 1 {
		t.Errorf("opencode rows after retry = %d, want 1", opencodeRows)
	}
	if s := pipeline.Stats(); s.Deduplicated != 1 {
		t.Errorf("deduplicated = %d, want 1", s.Deduplicated)
	}
}

func protoTraceRequest(t *testing.T, fixture string) *coltracepb.ExportTraceServiceRequest {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	u := &ptrace.JSONUnmarshaler{}
	td, err := u.UnmarshalTraces(data)
	if err != nil {
		t.Fatal(err)
	}
	er := ptraceotlp.NewExportRequestFromTraces(td)
	b, err := er.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	req := &coltracepb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		t.Fatal(err)
	}
	return req
}

func recount(t *testing.T, db *sql.DB, source string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations WHERE source = ?`, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
