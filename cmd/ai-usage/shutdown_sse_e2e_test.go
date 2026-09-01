package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: /events SSE handlers loop until the client disconnects and
// http.Server.Shutdown never cancels request contexts, so every graceful
// shutdown with an open dashboard tab used to burn the full grace period
// and log "http shutdown failed: context deadline exceeded". With the hub
// interrupt registered via RegisterOnShutdown, SIGTERM must still shut
// down cleanly and promptly while an SSE stream is open.
func TestShutdownWithOpenEventsStream(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	bin := filepath.Join(t.TempDir(), "ai-usage")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	dataDir := t.TempDir()
	httpPort, otlpPort := freePort(t), freePort(t)
	cmd, logs := startApp(t, bin, dataDir, httpPort, otlpPort)
	waitReady(t, httpPort)

	// Open an SSE stream the way a dashboard tab would and leave it open.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("http://127.0.0.1:%d/events", httpPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	stopApp(t, cmd, logs)

	out := logs.String()
	assertShutdownLogs(t, out)
	if strings.Contains(out, "http shutdown failed") {
		t.Errorf("shutdown must not wait out the grace period on SSE streams:\n%s", out)
	}
}
