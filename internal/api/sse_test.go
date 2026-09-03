package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

// Regression: accessLog wraps writers in statusWriter, which used to hide
// http.Flusher behind the embedded interface, so GET /events failed with
// 500 "streaming unsupported" in the real binary. The SSE handler must
// stream a data-changed frame through the full middleware chain.
func TestEventsThroughAccessLog(t *testing.T) {
	hub := live.New()
	h := NewWithAuth(seedtest.DB(t), testLogger(t), nil, nil, hub, "test", nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /events: status %d, want %d (body %q)", resp.StatusCode, http.StatusOK, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type %q, want text/event-stream", ct)
	}

	// Notify on a ticker: the handler subscribes asynchronously, so an
	// immediate fire could land before it is listening. Extra signals are
	// coalesced by the hub.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				hub.Notify()
			}
		}
	}()

	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(lines) == 0 {
				continue // keepalive comment frame
			}
			break
		}
		lines = append(lines, line)
	}
	if got := strings.Join(lines, "\n"); got != "event: data-changed\ndata: 1" {
		t.Errorf("event frame %q, want data-changed signal", got)
	}
}

// statusWriter must expose Flush to streaming handlers directly, not only
// via Unwrap + ResponseController.
func TestStatusWriterForwardsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	fl, ok := any(sw).(http.Flusher)
	if !ok {
		t.Fatal("statusWriter does not implement http.Flusher")
	}
	fl.Flush()
	if !rec.Flushed {
		t.Error("Flush was not forwarded to the underlying writer")
	}
}
