package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// TestRestartPersistence checks clean shutdown, migration idempotence, and
// deduplication across a process restart.
func TestRestartPersistence(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	bin := filepath.Join(t.TempDir(), "ai-usage")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.db")
	httpPort, otlpPort := freePort(t), freePort(t)

	start := func() (*exec.Cmd, *bytes.Buffer) {
		return startApp(t, bin, dataDir, httpPort, otlpPort)
	}

	// ---- first run: ingest one fixture batch
	cmd1, logs1 := start()
	waitReady(t, httpPort)

	fixture, err := os.ReadFile("../../testdata/copilot/traces-chat-simple.json")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/traces", otlpPort), "application/json", bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: status = %d, want 200", resp.StatusCode)
	}
	stored := statsStored(t, httpPort)
	if stored == 0 {
		t.Fatalf("stored = 0 after ingest")
	}

	stopApp(t, cmd1, logs1)
	assertShutdownLogs(t, logs1.String())

	// ---- second run: data survives the restart, re-ingest deduplicates
	cmd2, logs2 := start()
	waitReady(t, httpPort)

	if got := int64(apiGenerations(t, httpPort)); got != stored {
		t.Errorf("rows after restart = %d, want %d (rows must survive restart)", got, stored)
	}
	resp, err = http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/traces", otlpPort), "application/json", bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-ingest: status = %d, want 200", resp.StatusCode)
	}
	if got := int64(apiGenerations(t, httpPort)); got != stored {
		t.Errorf("rows after re-ingest = %d, want %d (DB-backed dedup across restarts)", got, stored)
	}
	if got := statsDeduplicated(t, httpPort); got != stored {
		t.Errorf("deduplicated counter after re-ingest = %d, want %d", got, stored)
	}

	stopApp(t, cmd2, logs2)

	// ---- final DB assertions: rows intact, migrations idempotent
	db, err := storage.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatalf("migrate on existing db: %v", err)
	}
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	var versions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 7 {
		t.Errorf("schema_migrations rows = %d, want 7", versions)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if int64(rows) != stored {
		t.Errorf("rows in db = %d, want %d", rows, stored)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startApp launches the built binary with the dashboard and OTLP HTTP
// listeners on the given ports (OTLP gRPC disabled), capturing stderr as
// the log buffer.
func startApp(t *testing.T, bin, dataDir string, httpPort, otlpPort int) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	cmd := exec.Command(bin,
		"--http", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--otlp-http", fmt.Sprintf("127.0.0.1:%d", otlpPort),
		"--otlp-grpc", "",
		"--data-dir", dataDir,
		"--database", filepath.Join(dataDir, "usage.db"),
	)
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &logs
}

// stopApp sends SIGTERM and expects a clean exit well inside the 10s
// shutdown grace period.
func stopApp(t *testing.T, cmd *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process did not exit cleanly: %v\nlogs:\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("process did not exit after SIGTERM\nlogs:\n%s", logs.String())
	}
}

func waitReady(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	url := fmt.Sprintf("http://127.0.0.1:%d/ready", port)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("app never became ready")
}

func statsStored(t *testing.T, port int) int64 {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/stats", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Stored int64 `json:"stored"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Stored
}

func statsDeduplicated(t *testing.T, port int) int64 {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/stats", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Deduplicated int64 `json:"deduplicated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Deduplicated
}

// apiGenerations counts the rows the API exposes for the DB-backed
// persistence assertions (pipeline counters reset on restart; rows do not).
func apiGenerations(t *testing.T, port int) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/generations?limit=500", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return len(body)
}

// assertShutdownLogs verifies the clean-shutdown log contract (JSON lines on
// stderr, message content only — no telemetry data).
func assertShutdownLogs(t *testing.T, logs string) {
	t.Helper()
	var started, complete bool
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		switch rec.Msg {
		case "shutdown started":
			started = true
		case "shutdown complete":
			complete = true
		}
	}
	if !started {
		t.Errorf(`logs missing "shutdown started" event:`+"\n%s", logs)
	}
	if !complete {
		t.Errorf(`logs missing "shutdown complete" event:`+"\n%s", logs)
	}
}
