package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/storage"
	"github.com/tbshfr/ai-usage/internal/storage/seedtest"
)

func TestBackupDashboardStatus(t *testing.T) {
	state := backup.Status{}
	handler := New(seedtest.DB(t), nil, nil, nil, "test", WithBackupStatus(func() backup.Status { return state }))
	render := func(path string) string {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", "http://localhost"+path, nil))
		if rr.Code != 200 {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	wantNotContains(t, render("/"), "id=\"backup-banner\"")
	state.Enabled = true
	wantContains(t, render("/"), "data-changed from:body delay:2s")
	wantNotContains(t, render("/"), "Waiting for first backup", "every 10s")
	wantContains(t, render("/settings"), "Waiting for first backup")
	wantNotContains(t, render("/settings/stats"), "Waiting for first backup", "Backup status")
	state.FailureStage = "upload"
	state.FailedAt = time.Now()
	body := render("/")
	wantContains(t, body, "Backup failed", "role=\"alert\"", `href="/settings#backups"`)
	if strings.Index(body, "Backup failed") > strings.Index(body, "id=\"filter-bar\"") {
		t.Fatal("banner is not above filters")
	}
	state.Running = true
	wantContains(t, render("/fragments/backup-banner"), "Backup failed", `href="/settings#backups"`)
	body = render("/fragments/backup-settings")
	wantContains(t, body, "Backup failed")
	wantNotContains(t, body, `href="/settings#backups"`)
	wantContains(t, render("/fragments/backup-settings"), "In progress")
	state = backup.Status{Enabled: true, LastSuccess: time.Now()}
	body = render("/fragments/backup-settings")
	wantContains(t, body, "Healthy", "Last successful backup")
	wantNotContains(t, body, "Backup failed")
	wantNotContains(t, render("/"), "Healthy", "Last successful backup", "Backup failed")
	if body := render("/fragments/backup-banner"); strings.TrimSpace(body) != "" {
		t.Fatalf("healthy banner: %s", body)
	}
}

func TestBackupSettings(t *testing.T) {
	state := backup.Status{}
	starts, reschedules := 0, 0
	srv := httptest.NewServer(New(seedtest.DB(t), nil, nil, nil, "test",
		WithBackupStatus(func() backup.Status { return state }),
		WithBackupActions(func() bool { starts++; return true }, func() { reschedules++ })))
	defer srv.Close()

	_, body, _ := do(t, srv, "GET", "/settings", "", nil)
	wantContains(t, body, "Backups are off", "AI_USAGE_BACKUP_S3_BUCKET=", "AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY=", "docs/backups.md")
	wantNotContains(t, body, "Back up now", "data-backup-time")
	if status, _, _ := do(t, srv, "POST", "/settings/backup", "", nil); status != http.StatusNotFound || starts != 0 {
		t.Fatalf("disabled start: %d, starts=%d", status, starts)
	}

	next := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	state = backup.Status{Enabled: true, LastSuccess: next.Add(-24 * time.Hour), NextRun: next}
	_, body, _ = do(t, srv, "GET", "/settings", "", nil)
	wantContains(t, body, `id="backups"`, "Healthy", "Next backup: 2026-09-09 03:00 UTC", "Back up now", "data-backup-time")
	wantNotContains(t, body, "Backups are off")
	status, _, resp := do(t, srv, "POST", "/settings/backup", "", nil)
	if status != http.StatusSeeOther || resp.Header.Get("Location") != "/settings#backups" || starts != 1 {
		t.Fatalf("start: %d %q, starts=%d", status, resp.Header.Get("Location"), starts)
	}
	if status, _, _ := do(t, srv, "POST", "/settings/backup", "", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"}); status != http.StatusForbidden || starts != 1 {
		t.Fatalf("cross-origin start: %d, starts=%d", status, starts)
	}

	state.Running = true
	_, body, _ = do(t, srv, "GET", "/fragments/backup-settings", "", nil)
	wantContains(t, body, "In progress", "Backing up…", "disabled")
	wantNotContains(t, body, "Next backup", "<html")

	status, response, _ := do(t, srv, "PUT", "/settings/preferences/backup", `{"time":" 3:05 ","timezone":"Europe/Berlin"}`, nil)
	if status != http.StatusNoContent || reschedules != 1 {
		t.Fatalf("save schedule: %d %s, reschedules=%d", status, response, reschedules)
	}
	if got := settingsResponse(t, srv, "").Backup; got != (storage.BackupSettings{Time: "03:05", Timezone: "Europe/Berlin"}) {
		t.Fatalf("saved schedule: %+v", got)
	}
	_, body, _ = do(t, srv, "GET", "/settings", "", nil)
	wantContains(t, body, `value="03:05"`, "Daily backup time<span data-backup-zone> (Europe/Berlin)</span>")
	wantNotContains(t, body, "Daily at")
	for _, invalid := range []string{
		`{"time":"25:00","timezone":"UTC"}`,
		`{"time":"3pm","timezone":"UTC"}`,
		`{"time":"03:00","timezone":"Nowhere/Invalid"}`,
		`{"time":"03:00","timezone":"Local"}`,
		`{"time":"","timezone":"UTC"}`,
	} {
		if status, _, _ := do(t, srv, "PUT", "/settings/preferences/backup", invalid, nil); status != http.StatusBadRequest {
			t.Errorf("invalid schedule %s: %d", invalid, status)
		}
	}
	if reschedules != 1 {
		t.Fatalf("rejected schedules rescheduled: %d", reschedules)
	}
	if status, _, _ := do(t, srv, "PUT", "/settings/preferences/backup", `{"time":"22:15","timezone":""}`, nil); status != http.StatusNoContent {
		t.Fatalf("schedule without zone: %d", status)
	}
	if got := settingsResponse(t, srv, "").Backup; got != (storage.BackupSettings{Time: "22:15", Timezone: "UTC"}) {
		t.Fatalf("schedule without zone: %+v", got)
	}
}
