package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/web"
)

// New returns the dashboard-port HTTP handler: liveness/readiness probes plus
// the JSON API routes from server.go, with debug-level access logging.
func New(db *sql.DB, logger *slog.Logger, stats StatsFunc, reasons ReasonCountsFunc, hub *live.Hub, version string) http.Handler {
	return NewWithAuth(db, logger, stats, reasons, hub, version, nil)
}

// NewWithAuth wraps the dashboard with the login-session guard when dash
// is non-nil; /health, /ready, /static and /login stay public.
func NewWithAuth(db *sql.DB, logger *slog.Logger, stats StatsFunc, reasons ReasonCountsFunc, hub *live.Hub, version string, dash *auth.Dashboard, backupStatus ...func() backup.Status) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	currentBackupStatus := func() backup.Status {
		if len(backupStatus) > 0 && backupStatus[0] != nil {
			return backupStatus[0]()
		}
		return backup.Status{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/backup", func(w http.ResponseWriter, r *http.Request) {
		s := currentBackupStatus()
		state, _ := backupStates(s)
		var lastSuccess, failedAt *time.Time
		if !s.LastSuccess.IsZero() {
			lastSuccess = &s.LastSuccess
		}
		if !s.FailedAt.IsZero() {
			failedAt = &s.FailedAt
		}
		writeJSON(w, http.StatusOK, struct {
			Status       string     `json:"status"`
			Enabled      bool       `json:"enabled"`
			Running      bool       `json:"running"`
			LastSuccess  *time.Time `json:"last_success"`
			FailedAt     *time.Time `json:"failed_at"`
			FailureStage string     `json:"failure_stage"`
		}{state, s.Enabled, s.Running, lastSuccess, failedAt, s.FailureStage})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		_, state := backupStates(currentBackupStatus())
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
			Backup string `json:"backup"`
		}{"ok", state})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("/api/", apiRoutes(db, stats, reasons, logger))
	if dash != nil {
		mux.Handle("/", web.NewAuthed(db, stats, reasons, dash, hub, version, backupStatus...))
	} else {
		mux.Handle("/", web.New(db, stats, reasons, hub, version, backupStatus...))
	}
	h := accessLog(logger, mux)
	if dash != nil {
		h = dash.Middleware(h)
	}
	return web.SecureHeaders(h)
}

// backupStates derives the detailed API state and the health summary together.
// Pending and running backups remain healthy unless an attempt has failed.
func backupStates(s backup.Status) (state, health string) {
	if !s.Enabled {
		return "disabled", "disabled"
	}
	if !s.FailedAt.IsZero() {
		return "failed", "unhealthy"
	}
	if s.Running {
		return "running", "healthy"
	}
	if s.LastSuccess.IsZero() {
		return "pending", "healthy"
	}
	return "ok", "healthy"
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsed := time.Since(start).Round(time.Microsecond)
		fields := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", elapsed.String(),
		}
		// Slow requests are the symptom of pool starvation or a hung
		// client; surface them at WARN so they are visible at the
		// default log level instead of hiding behind debug access logs.
		// The /events SSE stream is exempt: it stays open for as long
		// as the tab is foregrounded, so its duration means nothing.
		if r.URL.Path == "/events" {
			logger.Debug("http request", fields...)
			return
		}
		if elapsed > slowRequestThreshold {
			logger.Warn("slow http request", fields...)
			return
		}
		logger.Debug("http request", fields...)
	})
}

// slowRequestThreshold is how long a request may take before the access
// log escalates it from debug to warn.
const slowRequestThreshold = time.Second

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
	// Encode to a buffer first: json.Encode fails on non-finite floats
	// (+Inf/NaN). Writing the header first would send 200 with an empty
	// body; fail closed with 500 before anything is written instead.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}
