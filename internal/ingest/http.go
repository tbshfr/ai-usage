package ingest

import (
	"io"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type Receiver struct {
	logger *slog.Logger
}

func NewReceiver(logger *slog.Logger) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Receiver{logger: logger}
}

// Handler returns the OTLP/HTTP endpoints: POST /v1/traces, /v1/metrics, /v1/logs.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", r.handle(func(b []byte, encoding string) (int, error) {
		td, err := unmarshalTraces(b, encoding)
		if err != nil {
			return 0, err
		}
		return td.SpanCount(), nil
	}))
	mux.HandleFunc("POST /v1/metrics", r.handle(func(b []byte, encoding string) (int, error) {
		md, err := unmarshalMetrics(b, encoding)
		if err != nil {
			return 0, err
		}
		var n int
		for _, rm := range md.ResourceMetrics().All() {
			for _, sm := range rm.ScopeMetrics().All() {
				n += sm.Metrics().Len()
			}
		}
		return n, nil
	}))
	mux.HandleFunc("POST /v1/logs", r.handle(func(b []byte, encoding string) (int, error) {
		ld, err := unmarshalLogs(b, encoding)
		if err != nil {
			return 0, err
		}
		return ld.LogRecordCount(), nil
	}))
	return mux
}

func (r *Receiver) handle(count func(body []byte, encoding string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		encoding, ok := encodingOf(req.Header.Get("Content-Type"))
		if !ok {
			http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		n, err := count(body, encoding)
		if err != nil {
			r.logger.Warn("otlp decode failed", "error", err.Error())
			http.Error(w, "decode failed", http.StatusBadRequest)
			return
		}
		r.logger.Debug("otlp batch received",
			"signal", req.URL.Path,
			"encoding", encoding,
			"records", n,
		)
		w.WriteHeader(http.StatusOK)
	}
}

func encodingOf(contentType string) (string, bool) {
	switch contentType {
	case "application/x-protobuf":
		return "protobuf", true
	case "application/json":
		return "json", true
	default:
		return "", false
	}
}

func unmarshalTraces(b []byte, encoding string) (ptrace.Traces, error) {
	if encoding == "json" {
		u := &ptrace.JSONUnmarshaler{}
		return u.UnmarshalTraces(b)
	}
	u := ptrace.ProtoUnmarshaler{}
	return u.UnmarshalTraces(b)
}

func unmarshalMetrics(b []byte, encoding string) (pmetric.Metrics, error) {
	if encoding == "json" {
		u := &pmetric.JSONUnmarshaler{}
		return u.UnmarshalMetrics(b)
	}
	u := pmetric.ProtoUnmarshaler{}
	return u.UnmarshalMetrics(b)
}

func unmarshalLogs(b []byte, encoding string) (plog.Logs, error) {
	if encoding == "json" {
		u := &plog.JSONUnmarshaler{}
		return u.UnmarshalLogs(b)
	}
	u := plog.ProtoUnmarshaler{}
	return u.UnmarshalLogs(b)
}
