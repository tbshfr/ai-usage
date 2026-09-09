package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestBackupDashboardStatus(t *testing.T) {
	state := backup.Status{}
	handler := New(seedtest.DB(t), nil, nil, nil, "test", func() backup.Status { return state })
	render := func(path string) string {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	wantNotContains(t, render("/"), "id=\"backup-banner\"")
	state.Enabled = true
	wantContains(t, render("/"), "data-changed from:body delay:2s")
	wantNotContains(t, render("/"), "Waiting for first backup", "Backup status", "every 10s")
	wantContains(t, render("/stats"), "Waiting for first backup", "Backup status")
	state.FailureStage = "upload"
	state.FailedAt = time.Now()
	body := render("/")
	wantContains(t, body, "Backup failed", "role=\"alert\"")
	if strings.Index(body, "Backup failed") > strings.Index(body, "id=\"filter-bar\"") {
		t.Fatal("banner is not above filters")
	}
	state.Running = true
	wantContains(t, render("/fragments/backup-banner"), "Backup failed")
	wantContains(t, render("/fragments/stats"), "In progress")
	state = backup.Status{Enabled: true, LastSuccess: time.Now()}
	body = render("/fragments/stats")
	wantContains(t, body, "Healthy", "Last successful backup")
	wantNotContains(t, body, "Backup failed")
	wantNotContains(t, render("/"), "Healthy", "Last successful backup", "Backup failed")
	if body := render("/fragments/backup-banner"); strings.TrimSpace(body) != "" {
		t.Fatalf("healthy banner: %s", body)
	}
}
