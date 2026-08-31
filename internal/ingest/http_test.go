package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tbshfr/ai-usage/internal/auth"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestReceiverAcceptsFixtures(t *testing.T) {
	r := NewReceiver(&stubConsumer{}, slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	cases := []struct {
		name  string
		path  string
		file  string
		ctype string
	}{
		{"copilot json traces", "/v1/traces", "../../testdata/copilot/traces-chat-simple.json", "application/json"},
		{"opencode json traces", "/v1/traces", "../../testdata/opencode/traces-llm.json", "application/json"},
		{"opencode pb traces", "/v1/traces", "../../testdata/opencode/traces-llm.pb", "application/x-protobuf"},
		{"copilot metrics", "/v1/metrics", "../../testdata/copilot/metrics.json", "application/json"},
		{"opencode logs", "/v1/logs", "../../testdata/opencode/logs.json", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Post(srv.URL+tc.path, tc.ctype, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

func TestReceiverBearerToken(t *testing.T) {
	r := NewReceiver(&stubConsumer{}, nil)
	h := auth.Bearer(nil, "s3cret", r.Handler())
	srv := httptest.NewServer(h)
	defer srv.Close()

	body, err := os.ReadFile("../../testdata/opencode/traces-llm.json")
	if err != nil {
		t.Fatal(err)
	}
	post := func(token string) int {
		req, err := http.NewRequest("POST", srv.URL+"/v1/traces", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(""); got != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", got)
	}
	if got := post("Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", got)
	}
	if got := post("Bearer s3cret"); got != http.StatusOK {
		t.Errorf("valid token: status = %d, want 200", got)
	}
}

func TestReceiverRejectsBadPayload(t *testing.T) {
	r := NewReceiver(&stubConsumer{}, nil)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/traces", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestReceiverRejectsUnknownContentType(t *testing.T) {
	r := NewReceiver(&stubConsumer{}, nil)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/traces", "text/plain", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}

// Every non-200 OTLP/HTTP response must be logged with its status code —
// clients (opencode, VS Code) do not surface OTLP status codes themselves.
func TestReceiverLogsRejections(t *testing.T) {
	var logs bytes.Buffer
	r := NewReceiver(&stubConsumer{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	cases := []struct {
		name       string
		path       string
		ctype      string
		encoding   string
		body       string
		wantStatus int
	}{
		{"missing content type", "/v1/traces", "", "", `{"sensitive":"PROMPT-CONTENT-MARKER"}`, http.StatusBadRequest},
		{"unsupported content type", "/v1/traces", "text/plain", "", `{"sensitive":"PROMPT-CONTENT-MARKER"}`, http.StatusUnsupportedMediaType},
		{"unsupported content encoding", "/v1/traces", "application/json", "zstd", `{"sensitive":"PROMPT-CONTENT-MARKER"}`, http.StatusUnsupportedMediaType},
		{"malformed payload", "/v1/traces", "application/json", "", `{"sensitive":"PROMPT-CONTENT-MARKER"`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			req, err := http.NewRequest(http.MethodPost, srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.ctype != "" {
				req.Header.Set("Content-Type", tc.ctype)
			}
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			var rec struct {
				Msg    string `json:"msg"`
				Signal string `json:"signal"`
				Status int    `json:"status"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &rec); err != nil {
				t.Fatalf("rejection not logged as JSON: %v\nlogs:\n%s", err, logs.String())
			}
			if rec.Msg != "otlp request rejected" || rec.Signal != "traces" || rec.Status != tc.wantStatus {
				t.Errorf("log = %+v, want msg=otlp request rejected signal=traces status=%d", rec, tc.wantStatus)
			}
			if strings.Contains(logs.String(), "PROMPT-CONTENT-MARKER") {
				t.Error("log must not contain request payload content")
			}
		})
	}
}

func TestReceiverReturns503WhenPipelineFails(t *testing.T) {
	r := NewReceiver(&failingConsumer{}, nil)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	fixture, err := os.ReadFile("../../testdata/copilot/traces-chat-simple.json")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v1/traces", "application/json", bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

type stubConsumer struct{}

func (stubConsumer) ConsumeTraces(context.Context, ptrace.Traces) error    { return nil }
func (stubConsumer) ConsumeMetrics(context.Context, pmetric.Metrics) error { return nil }
func (stubConsumer) ConsumeLogs(context.Context, plog.Logs) error          { return nil }

type failingConsumer struct{}

func (failingConsumer) ConsumeTraces(context.Context, ptrace.Traces) error {
	return errors.New("db down")
}
func (failingConsumer) ConsumeMetrics(context.Context, pmetric.Metrics) error { return nil }
func (failingConsumer) ConsumeLogs(context.Context, plog.Logs) error          { return nil }
