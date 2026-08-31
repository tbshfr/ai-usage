package ingest

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestReceiverAcceptsFixtures(t *testing.T) {
	r := NewReceiver(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
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

func TestReceiverRejectsBadPayload(t *testing.T) {
	r := NewReceiver(nil)
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
	r := NewReceiver(nil)
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
