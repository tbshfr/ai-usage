package web

import (
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	sseEventName  = "data-changed"
	sseKeepalive  = 20 * time.Second
	sseEventFrame = "event: " + sseEventName + "\ndata: 1\n\n"
	ssePingFrame  = ": keepalive\n\n"
)

// serveEvents streams the dashboard's Server-Sent Events feed. The stream
// carries no page content: a named data-changed event only signals "new
// generations were stored", and the htmx SSE extension re-fetches whatever
// fragments the current page renders (with the client's own filter state).
// Dropped events are harmless — the next one follows, and the response is
// one fragment re-render instead of pushed HTML.
func (s *server) serveEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// The dashboard server sets WriteTimeout (60s), which would kill every
	// long-lived stream; clear the write deadline for this connection.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		slog.Error("sse deadline setup failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	var events <-chan struct{}
	if s.hub != nil {
		var cancel func()
		events, cancel = s.hub.Subscribe()
		defer cancel()
	}
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-events:
			if _, err := io.WriteString(w, sseEventFrame); err != nil {
				return
			}
			fl.Flush()
		case <-keepalive.C:
			if _, err := io.WriteString(w, ssePingFrame); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
