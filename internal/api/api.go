package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/web"
)

// New returns the dashboard-port HTTP handler: liveness/readiness probes plus
// the JSON API routes from server.go, with debug-level access logging.
func New(db *sql.DB, logger *slog.Logger, stats StatsFunc, hub *live.Hub, version string) http.Handler {
	return NewWithAuth(db, logger, stats, hub, version, nil)
}

// NewWithAuth wraps the dashboard with the login-session guard when dash
// is non-nil; /health, /ready, /static and /login stay public.
func NewWithAuth(db *sql.DB, logger *slog.Logger, stats StatsFunc, hub *live.Hub, version string, dash *auth.Dashboard) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("/api/", apiRoutes(db, stats, logger))
	if dash != nil {
		mux.Handle("/", web.NewAuthed(db, dash, hub, version))
	} else {
		mux.Handle("/", web.New(db, hub, version))
	}
	h := accessLog(logger, mux)
	if dash != nil {
		h = dash.Middleware(h)
	}
	return web.SecureHeaders(h)
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush forwards http.Flusher so wrapped streaming handlers (the /events SSE
// feed) can push bytes immediately; the embedded ResponseWriter interface
// does not expose the underlying connection's Flush method.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.NewResponseController reach the underlying writer, so
// the /events SSE stream can clear the server's WriteTimeout.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
