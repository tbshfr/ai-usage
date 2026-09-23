// Command capture is the local OTLP/HTTP receiver used to refresh fixtures.
// See docs/development.md for the capture and sanitization workflow.
//
// It listens on 127.0.0.1:4318 for POST /v1/traces, /v1/metrics, /v1/logs
// and writes every received batch to disk unchanged, so it can later be
// sanitized into testdata/ fixtures with cmd/sanitize.
//
// Usage:
//
//	go run ./cmd/capture /tmp/ai-usage-capture
//
// Each request is stored as <dir>/<kind>-<unixmilli>-<n>.<ext> where kind
// is one of traces, metrics, logs, unixmilli is the receive time, n is a
// monotonically increasing counter (6 digits), and ext is json when
// Content-Type is application/json, pb otherwise. Never commit unsanitized
// captures.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var counter atomic.Uint64

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: capture <dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "capture:", err)
		os.Exit(1)
	}
	for _, route := range []struct {
		path string
		kind string
	}{{"/v1/traces", "traces"}, {"/v1/metrics", "metrics"}, {"/v1/logs", "logs"}} {
		kind := route.kind
		http.HandleFunc(route.path, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			n := counter.Add(1)
			name := fmt.Sprintf("%s/%s-%d-%06d.%s", dir, kind, time.Now().UnixMilli(), n, encoding(r))
			if err := os.WriteFile(filepath.Clean(name), body, 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "capture:", err)
			} else {
				fmt.Printf("captured %s %d bytes -> %s\n", kind, len(body), name)
			}
			w.WriteHeader(200)
		})
	}
	fmt.Println("listening on 127.0.0.1:4318, writing to", dir)
	if err := http.ListenAndServe("127.0.0.1:4318", nil); err != nil {
		fmt.Fprintln(os.Stderr, "capture:", err)
		os.Exit(1)
	}
}

func encoding(r *http.Request) string {
	if r.Header.Get("Content-Type") == "application/json" {
		return "json"
	}
	return "pb"
}
