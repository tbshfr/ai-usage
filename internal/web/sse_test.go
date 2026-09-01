package web

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

func TestEventsRequiresSession(t *testing.T) {
	srv, _ := newAuthedServer(t)
	status, _, resp := do(t, srv, "GET", "/events", "", nil)
	if status != http.StatusSeeOther {
		t.Errorf("GET /events without session: status %d, want %d", status, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location %q, want /login", loc)
	}
}

func TestEventsSignalsDataChanged(t *testing.T) {
	hub := live.New()
	srv := httptestServer(t, hub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache", cc)
	}

	// Notify on a ticker: the handler subscribes asynchronously, so an
	// immediate fire could land before it is listening. Extra signals are
	// coalesced, and later ones just repeat the same frame.
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

	frame := readEventFrame(t, resp.Body)
	if frame != "event: data-changed\ndata: 1" {
		t.Errorf("event frame %q, want data-changed signal", frame)
	}
}

// readEventFrame reads one complete SSE frame (up to the blank line that
// terminates it) and returns the frame without the blank line.
func readEventFrame(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(lines) == 0 {
				continue // keepalive comment frame: ": keepalive" then blank
			}
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
	t.Fatalf("stream ended without an event frame (lines: %q)", lines)
	return ""
}

func TestEventsClientDisconnectCleansUp(t *testing.T) {
	hub := live.New()
	srv := httptestServer(t, hub)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Disconnecting must end the handler (its unsubscribe runs via defer);
	// after the client goes away the connection read fails server-side.
	cancel()
	if _, err := resp.Body.Read(make([]byte, 16)); err == nil {
		t.Error("read after cancel succeeded, want error")
	}

	// The server stays healthy for new requests.
	status, _ := get(t, srv.URL+"/")
	if status != http.StatusOK {
		t.Errorf("dashboard after SSE disconnect: status %d, want %d", status, http.StatusOK)
	}
}

// httptestServer builds an unauthenticated UI server on the given hub.
func httptestServer(t *testing.T, hub *live.Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(seedtest.DB(t), hub, "test"))
	t.Cleanup(srv.Close)
	return srv
}
