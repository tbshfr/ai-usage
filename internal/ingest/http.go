package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Consumer is the interface both receivers feed. Implementations must be
// safe for concurrent use.
type Consumer interface {
	ConsumeTraces(ctx context.Context, td ptrace.Traces) error
	ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error
	ConsumeLogs(ctx context.Context, ld plog.Logs) error
}

type Receiver struct {
	consumer Consumer
	logger   *slog.Logger
}

func NewReceiver(consumer Consumer, logger *slog.Logger) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Receiver{consumer: consumer, logger: logger}
}

// Handler returns the OTLP/HTTP endpoints: POST /v1/traces, /v1/metrics, /v1/logs.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", r.handle("traces", func(b []byte, encoding string) (int, error) {
		td, err := unmarshalTraces(b, encoding)
		if err != nil {
			return 0, err
		}
		if err := r.consumer.ConsumeTraces(context.Background(), td); err != nil {
			return 0, consumeError{err}
		}
		return td.SpanCount(), nil
	}))
	mux.HandleFunc("POST /v1/metrics", r.handle("metrics", func(b []byte, encoding string) (int, error) {
		md, err := unmarshalMetrics(b, encoding)
		if err != nil {
			return 0, err
		}
		if err := r.consumer.ConsumeMetrics(context.Background(), md); err != nil {
			return 0, consumeError{err}
		}
		var n int
		for _, rm := range md.ResourceMetrics().All() {
			for _, sm := range rm.ScopeMetrics().All() {
				n += sm.Metrics().Len()
			}
		}
		return n, nil
	}))
	mux.HandleFunc("POST /v1/logs", r.handle("logs", func(b []byte, encoding string) (int, error) {
		ld, err := unmarshalLogs(b, encoding)
		if err != nil {
			return 0, err
		}
		if err := r.consumer.ConsumeLogs(context.Background(), ld); err != nil {
			return 0, consumeError{err}
		}
		return ld.LogRecordCount(), nil
	}))
	return mux
}

// consumeError distinguishes pipeline failures (retryable, 503) from
// malformed payloads (client error, 400).
type consumeError struct{ err error }

func (c consumeError) Error() string { return c.err.Error() }

func (r *Receiver) handle(signal string, process func(body []byte, encoding string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Content-Type") == "" {
			http.Error(w, "missing content type", http.StatusBadRequest)
			return
		}
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
		switch ce := req.Header.Get("Content-Encoding"); ce {
		case "", "identity":
		case "gzip":
			gz, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
			body, err = io.ReadAll(gz)
			gz.Close()
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unsupported content encoding", http.StatusUnsupportedMediaType)
			return
		}
		n, err := process(body, encoding)
		if err != nil {
			var ce consumeError
			if errors.As(err, &ce) {
				r.logger.Error("pipeline failed", "signal", signal, "error", ce.err.Error())
				http.Error(w, "ingestion unavailable", http.StatusServiceUnavailable)
				return
			}
			r.logger.Warn("otlp decode failed", "signal", signal, "error", err.Error())
			http.Error(w, "decode failed", http.StatusBadRequest)
			return
		}
		r.logger.Debug("otlp batch received",
			"signal", signal,
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
