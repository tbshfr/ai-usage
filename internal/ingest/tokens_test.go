package ingest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TestTokenAttribution sends exports over HTTP (traces and logs) and gRPC
// with different tokens and checks every stored row carries the token
// that authenticated it.
func TestTokenAttribution(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	store, err := auth.NewTokenStore(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	work, workID, err := store.Create(ctx, "laptop", "work")
	if err != nil {
		t.Fatal(err)
	}
	private, privateID, err := store.Create(ctx, "machine1", "private")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := NewPipeline(db, nil, nil)

	srv := httptest.NewServer(auth.Bearer(nil, store.ForListener(false), NewReceiver(pipeline, nil).Handler()))
	defer srv.Close()
	post := func(path, file, token string) {
		t.Helper()
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", file, resp.StatusCode)
		}
	}
	post("/v1/traces", "../../testdata/opencode/traces-llm.json", work)
	post("/v1/logs", "../../testdata/claude/logs-api-request.json", private)

	server := NewGRPCServer(pipeline, nil, store.ForListener(false), nil)
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
	mdCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+private)
	if _, err := coltracepb.NewTraceServiceClient(conn).Export(mdCtx, protoTraceRequest(t, "../../testdata/copilot/traces-chat-simple.json")); err != nil {
		t.Fatal(err)
	}

	want := map[string]int64{"opencode": workID, "claude-code": privateID, "copilot": privateID}
	rows, err := db.Query(`SELECT source, COALESCE(token_id, 0), COUNT(*) FROM generations GROUP BY source, token_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var source string
		var id, n int64
		if err := rows.Scan(&source, &id, &n); err != nil {
			t.Fatal(err)
		}
		if seen[source] {
			t.Errorf("%s rows split across tokens", source)
		}
		seen[source] = true
		if want[source] != id {
			t.Errorf("%s rows attributed to token %d, want %d", source, id, want[source])
		}
	}
	for source := range want {
		if !seen[source] {
			t.Errorf("no %s rows stored", source)
		}
	}
}
