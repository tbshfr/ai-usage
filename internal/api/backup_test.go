package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestBackupStatus(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	state := backup.Status{}
	handler := NewWithAuth(seedtest.DB(t), testLogger(t), nil, nil, nil, "test", nil, func() backup.Status { return state })
	for _, tc := range []struct {
		name   string
		health string
		state  backup.Status
	}{
		{"disabled", "disabled", backup.Status{}},
		{"pending", "healthy", backup.Status{Enabled: true}},
		{"running", "healthy", backup.Status{Enabled: true, Running: true}},
		{"running", "healthy", backup.Status{Enabled: true, Running: true, LastSuccess: now}},
		{"ok", "healthy", backup.Status{Enabled: true, LastSuccess: now}},
		{"failed", "unhealthy", backup.Status{Enabled: true, LastSuccess: now, FailedAt: now, FailureStage: "upload"}},
		{"failed", "unhealthy", backup.Status{Enabled: true, Running: true, FailedAt: now, FailureStage: "upload"}},
		{"ok", "healthy", backup.Status{Enabled: true, LastSuccess: now}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state = tc.state
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/backup", nil))
			var got struct {
				Status       string     `json:"status"`
				Enabled      bool       `json:"enabled"`
				Running      bool       `json:"running"`
				LastSuccess  *time.Time `json:"lastSuccess"`
				FailedAt     *time.Time `json:"failedAt"`
				FailureStage string     `json:"failureStage"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || got.Status != tc.name || got.Enabled != state.Enabled || got.Running != state.Running || got.FailureStage != state.FailureStage {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			for _, pair := range []struct {
				got  *time.Time
				want time.Time
			}{{got.LastSuccess, state.LastSuccess}, {got.FailedAt, state.FailedAt}} {
				if pair.want.IsZero() {
					if pair.got != nil {
						t.Fatal("unset timestamp must be null")
					}
				} else if pair.got == nil || !pair.got.Equal(pair.want) {
					t.Fatal("timestamp mismatch")
				}
			}
			for _, path := range []string{"/health", "/ready"} {
				probe := httptest.NewRecorder()
				handler.ServeHTTP(probe, httptest.NewRequest("GET", path, nil))
				if probe.Code != 200 {
					t.Fatalf("%s: %d", path, probe.Code)
				}
				if path == "/health" {
					var health map[string]string
					if err := json.Unmarshal(probe.Body.Bytes(), &health); err != nil {
						t.Fatal(err)
					}
					if health["status"] != "ok" || health["backup"] != tc.health {
						t.Fatalf("unexpected health: %s", probe.Body.String())
					}
				}
			}
		})
	}
}

func TestBackupStatusWithoutWorker(t *testing.T) {
	handler := New(seedtest.DB(t), testLogger(t), nil, nil, nil, "test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/backup", nil))
	if w.Code != 200 || w.Body.String() != "{\"status\":\"disabled\",\"enabled\":false,\"running\":false,\"lastSuccess\":null,\"failedAt\":null,\"failureStage\":\"\"}\n" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
