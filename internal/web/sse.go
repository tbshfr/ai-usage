package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	sseEventName    = "data-changed"
	sseKeepalive    = 20 * time.Second
	sseHelloPadding = 2 * 1024
	sseEventFrame   = "event: " + sseEventName + "\ndata: 1\n\n"
	ssePingFrame    = ": keepalive\n\n"
)

var sseHelloFrame = ":" + strings.Repeat(" ", sseHelloPadding) + "\n\n"

// serveEvents streams the dashboard's Server-Sent Events feed. The stream
// carries no page content: a named data-changed event only signals "displayed data or backup status changed", and the htmx SSE extension re-fetches whatever
// fragments the current page renders (with the client's own filter state).
// Dropped events are harmless — the next one follows, and the response is
// one fragment re-render instead of pushed HTML.
func (s *server) serveEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The dashboard server sets WriteTimeout (60s), which would kill every
	// long-lived stream; clear the write deadline for this connection.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Error("sse deadline setup failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The stream is tracked on the hub before the response starts so a
	// graceful server shutdown can cancel it: http.Server.Shutdown waits
	// for active connections but never cancels request contexts, so an
	// open dashboard tab would otherwise block the shutdown until the
	// grace period expires.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	untrack := func() {}
	if s.hub != nil {
		untrack = s.hub.TrackStream(cancel)
	}
	defer untrack()

	var events <-chan struct{}
	if s.hub != nil {
		var subCancel func()
		events, subCancel = s.hub.Subscribe()
		defer subCancel()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Include enough body data in the first flush to cross buffering thresholds
	// in browsers and intermediaries. The SSE parser ignores comment frames.
	if _, err := io.WriteString(w, sseHelloFrame); err != nil {
		return
	}
	// Flush through ResponseController, not a direct http.Flusher assertion:
	// ResponseController calls Flush on the outer writer when available, and
	// otherwise unwraps middleware wrappers that expose it.
	if err := rc.Flush(); err != nil {
		slog.Error("sse flush unsupported", "error", err.Error())
		return
	}

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			if _, err := io.WriteString(w, sseEventFrame); err != nil {
				return
			}
			_ = rc.Flush()
		case <-keepalive.C:
			if _, err := io.WriteString(w, ssePingFrame); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}
