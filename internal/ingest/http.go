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

// RejectCounter is implemented by consumers that track transport-level
// rejections (auth failures, malformed requests) which never reach the
// pipeline's record counters.
type RejectCounter interface {
	BumpHTTPReject(reason string)
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

// maxBodyBytes bounds the request body (compressed) and the decompressed
// gzip payload, so a large or malicious export cannot exhaust memory.
const maxBodyBytes = 32 << 20

func (r *Receiver) handle(signal string, process func(body []byte, encoding string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		reject := func(status int, msg, reason string) {
			// Count the rejection under its fixed-enum reason so the
			// stats breakdown shows what arrived and why it never became
			// a record; unauthenticated traffic only bumps integers.
			if rc, ok := r.consumer.(RejectCounter); ok {
				rc.BumpHTTPReject(reason)
			}
			// Clients (opencode, VS Code) do not surface OTLP status codes,
			// so every non-200 response is logged to make rejections visible.
			r.logger.Warn("otlp request rejected", "signal", signal, "status", status, "reason", reason)
			http.Error(w, msg, status)
		}
		if req.Header.Get("Content-Type") == "" {
			reject(http.StatusBadRequest, "missing content type", ReasonBadContentType)
			return
		}
		encoding, ok := encodingOf(req.Header.Get("Content-Type"))
		if !ok {
			reject(http.StatusUnsupportedMediaType, "unsupported content type", ReasonBadContentType)
			return
		}
		req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				reject(http.StatusRequestEntityTooLarge, "request body too large", ReasonBodyTooLarge)
				return
			}
			reject(http.StatusBadRequest, "read body", ReasonBodyReadError)
			return
		}
		switch ce := req.Header.Get("Content-Encoding"); ce {
		case "", "identity":
		case "gzip":
			gz, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				reject(http.StatusBadRequest, "bad gzip body", ReasonBadGzip)
				return
			}
			body, err = io.ReadAll(io.LimitReader(gz, maxBodyBytes+1))
			gz.Close()
			if err != nil {
				reject(http.StatusBadRequest, "bad gzip body", ReasonBadGzip)
				return
			}
			if len(body) > maxBodyBytes {
				reject(http.StatusRequestEntityTooLarge, "request body too large", ReasonBodyTooLarge)
				return
			}
		default:
			reject(http.StatusUnsupportedMediaType, "unsupported content encoding", ReasonBadEncoding)
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
			// No error detail in the log: decode errors can echo payload
			// fragments, and prompt/completion content must never be logged.
			reject(http.StatusBadRequest, "decode failed", ReasonDecodeFailed)
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
