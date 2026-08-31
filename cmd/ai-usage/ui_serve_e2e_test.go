package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Phase 6 acceptance criterion: run the binary from an empty working
// directory and verify the full UI (pages, fragments, static assets) is
// served — everything must come from the embedded assets.
func TestServesUIFromEmptyWorkingDirectory(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	bin := filepath.Join(t.TempDir(), "ai-usage")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	emptyDir := t.TempDir()
	dataDir := t.TempDir()
	httpPort, otlpPort := freePort(t), freePort(t)

	logs := &syncBuffer{}
	cmd := exec.Command(bin,
		"--http", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--otlp-http", fmt.Sprintf("127.0.0.1:%d", otlpPort),
		"--otlp-grpc", "",
		"--data-dir", dataDir,
		"--database", filepath.Join(dataDir, "usage.db"),
	)
	cmd.Dir = emptyDir
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	waitReady(t, httpPort)

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	cases := []struct {
		path        string
		wantStatus  int
		wantInBody  string
		contentType string
	}{
		{path: "/", wantStatus: http.StatusOK, wantInBody: "<html"},
		{path: "/breakdowns", wantStatus: http.StatusOK, wantInBody: "<html"},
		{path: "/generations", wantStatus: http.StatusOK, wantInBody: "<html"},
		{path: "/generations/nonexistent-id", wantStatus: http.StatusNotFound},
		{path: "/fragments/overview-cards", wantStatus: http.StatusOK, wantInBody: "<div"},
		{path: "/api/summary", wantStatus: http.StatusOK, contentType: "application/json"},
		{path: "/static/app.css", wantStatus: http.StatusOK, contentType: "text/css"},
		{path: "/static/app.js", wantStatus: http.StatusOK, contentType: "text/javascript"},
		{path: "/static/vendor/htmx.min.js", wantStatus: http.StatusOK, contentType: "text/javascript"},
		{path: "/static/vendor/uplot.min.js", wantStatus: http.StatusOK, contentType: "text/javascript"},
		{path: "/static/vendor/uplot.min.css", wantStatus: http.StatusOK, contentType: "text/css"},
	}
	for _, tc := range cases {
		resp, err := http.Get(base + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		var body bytes.Buffer
		body.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Errorf("GET %s: status = %d, want %d", tc.path, resp.StatusCode, tc.wantStatus)
			continue
		}
		if tc.wantInBody != "" && !strings.Contains(body.String(), tc.wantInBody) {
			t.Errorf("GET %s: body does not contain %q", tc.path, tc.wantInBody)
		}
		if tc.contentType != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.contentType) {
			t.Errorf("GET %s: content type = %q, want prefix %q", tc.path, resp.Header.Get("Content-Type"), tc.contentType)
		}
	}

	summaryResp, err := http.Get(base + "/api/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer summaryResp.Body.Close()
	var summary map[string]any
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Errorf("/api/summary is not valid JSON: %v", err)
	}

	stopProcess(t, cmd, logs)
}

// Listener gating: an empty address must disable that listener.
func TestEmptyAddrDisablesListener(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	bin := filepath.Join(t.TempDir(), "ai-usage")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	dataDir := t.TempDir()
	otlpPort := freePort(t)

	logs := &syncBuffer{}
	cmd := exec.Command(bin,
		"--http", "",
		"--otlp-http", fmt.Sprintf("127.0.0.1:%d", otlpPort),
		"--otlp-grpc", "",
		"--data-dir", dataDir,
		"--database", filepath.Join(dataDir, "usage.db"),
	)
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	waitOTLP(t, otlpPort)

	var listenerStarts int
	for _, line := range strings.Split(logs.String(), "\n") {
		var rec struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err == nil && rec.Msg == "http listener started" {
			listenerStarts++
		}
	}
	if listenerStarts != 1 {
		t.Errorf("http listener started events = %d, want 1 (only OTLP HTTP; --http \"\" must disable the dashboard)", listenerStarts)
	}
	if strings.Contains(logs.String(), "grpc listener started") {
		t.Error("grpc listener started despite --otlp-grpc \"\"")
	}

	fixture, err := fixtureBytes("../../testdata/copilot/traces-chat-simple.json")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/traces", otlpPort), "application/json", bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ingest with dashboard disabled: status = %d, want 200", resp.StatusCode)
	}

	stopProcess(t, cmd, logs)
}

// stopProcess sends SIGTERM and requires a clean exit with clean-shutdown logs.
func stopProcess(t *testing.T, cmd *exec.Cmd, logs *syncBuffer) {
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
	assertShutdownLogs(t, logs.String())
}

// waitOTLP polls until the OTLP endpoint accepts connections.
func waitOTLP(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port)
	for time.Now().Before(deadline) {
		resp, err := http.Post(url, "application/json", bytes.NewReader([]byte("{}")))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("otlp endpoint never became reachable")
}

func fixtureBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// syncBuffer is a concurrency-safe log sink: the subprocess keeps writing
// stderr while the test reads the log lines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
