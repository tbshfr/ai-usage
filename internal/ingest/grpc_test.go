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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	pipeline := NewPipeline(db, nil, nil)
	server := NewGRPCServer(pipeline, nil, "")

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

func TestGRPCTokenRequired(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	server := NewGRPCServer(NewPipeline(db, nil, nil), nil, "s3cret")
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

	if _, err := client.Export(ctx, req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("export without token: code = %v, want Unauthenticated", status.Code(err))
	}
	mdCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer s3cret")
	if _, err := client.Export(mdCtx, req); err != nil {
		t.Fatalf("export with token: %v", err)
	}
	badCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer wrong")
	if _, err := client.Export(badCtx, req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("export with wrong token: code = %v, want Unauthenticated", status.Code(err))
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
