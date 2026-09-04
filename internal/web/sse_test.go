package web

import (
	"bufio"
	"context"
	"io"
	"net"
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

	hub.Notify()
	frame := readEventFrame(t, resp.Body)
	if frame != "event: data-changed\ndata: 1" {
		t.Errorf("event frame %q, want data-changed signal", frame)
	}
}

// Regression: once response headers expose the stream to the browser, the
// hub subscription must already exist. Otherwise an ingest notification that
// races with connection startup is dropped until some later ingest arrives.
func TestEventsSubscribesBeforeFirstFlush(t *testing.T) {
	hub := live.New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	recorder := &notifyOnFirstFlushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFirstFlush:     hub.Notify,
	}
	recorder.afterFlush = func() {
		if strings.Contains(recorder.Body.String(), sseEventFrame) {
			cancel()
		}
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/events", nil)
	(&server{hub: hub}).serveEvents(recorder, req)

	if !strings.Contains(recorder.Body.String(), sseEventFrame) {
		t.Fatalf("response %q does not contain notification fired on first flush", recorder.Body.String())
	}
}

type notifyOnFirstFlushRecorder struct {
	*httptest.ResponseRecorder
	onFirstFlush func()
	afterFlush   func()
	flushed      bool
}

func (w *notifyOnFirstFlushRecorder) Flush() {
	if !w.flushed {
		w.flushed = true
		w.onFirstFlush()
	}
	w.ResponseRecorder.Flush()
	if w.afterFlush != nil {
		w.afterFlush()
	}
}

func (w *notifyOnFirstFlushRecorder) SetWriteDeadline(time.Time) error { return nil }

// Regression: intermediaries with short idle timeouts treat a stream that
// only has headers as dead; the first body bytes must go on the wire
// immediately, before any event fires and before the 20s keepalive tick.
func TestEventsSendsHelloFrameImmediately(t *testing.T) {
	hub := live.New()
	srv := httptestServer(t, hub)

	req, err := http.NewRequest("GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read the first bytes with a deadline far below sseKeepalive: the
	// hello frame must already be buffered, not arrive on the tick.
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, len(sseHelloFrame))
		n, err := io.ReadFull(resp.Body, buf)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("reading hello frame: %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hello frame within 2s; first bytes not sent immediately")
	}

	// The hello frame must not surface as an event: it is a comment, and
	// the next real frame still needs a hub notification.
	hub.Notify()
	frame := readEventFrame(t, resp.Body)
	if frame != "event: data-changed\ndata: 1" {
		t.Errorf("event frame %q, want data-changed signal", frame)
	}
}

// readEventFrame reads one complete SSE frame (up to the blank line that
// terminates it) and returns the frame without the blank line. Comment
// frames (": ..." — hello and keepalive pings) are skipped.
func readEventFrame(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(lines) == 0 || strings.HasPrefix(lines[0], ":") {
				lines = lines[:0] // comment frame: skip it
				continue
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
	// The hello frame may already be buffered client-side, so reads drain
	// it first — but they must eventually fail, never end cleanly at EOF.
	cancel()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("read after cancel succeeded, want error")
	}

	// The server stays healthy for new requests.
	status, _ := get(t, srv.URL+"/")
	if status != http.StatusOK {
		t.Errorf("dashboard after SSE disconnect: status %d, want %d", status, http.StatusOK)
	}
}

// Regression: http.Server.Shutdown waits for active connections and never
// cancels request contexts, so an open /events stream used to block every
// graceful shutdown until the grace period expired. Shutdown must return
// promptly while a stream is open, because the hub interrupt (registered
// via RegisterOnShutdown in main) ends it.
func TestShutdownDoesNotWaitForOpenEventsStream(t *testing.T) {
	hub := live.New()
	handler := New(seedtest.DB(t), nil, nil, hub, "test")
	hs := &http.Server{Handler: handler}
	// Mirrors main.go: the interrupt ends tracked SSE streams when the
	// graceful shutdown begins.
	hs.RegisterOnShutdown(hub.InterruptStreams)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go hs.Serve(ln) //nolint:errcheck — Serve returns ErrServerClosed on Shutdown
	defer hs.Close()

	resp, err := http.Get("http://" + ln.Addr().String() + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %q, want text/event-stream", ct)
	}

	done := make(chan error, 1)
	go func() { done <- hs.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown with an open SSE stream: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return while an SSE stream was open")
	}
}

// httptestServer builds an unauthenticated UI server on the given hub.
func httptestServer(t *testing.T, hub *live.Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(seedtest.DB(t), nil, nil, hub, "test"))
	t.Cleanup(srv.Close)
	return srv
}
