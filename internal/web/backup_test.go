package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/live"
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

func TestBackupList(t *testing.T) {
	state := backup.Status{}
	var objects []backup.Object
	var listErr error
	srv := httptest.NewServer(New(seedtest.DB(t), nil, nil, nil, "test",
		WithBackupStatus(func() backup.Status { return state }),
		WithBackupFiles(func(context.Context) ([]backup.Object, error) { return objects, listErr }, nil)))
	defer srv.Close()

	if status, _, _ := do(t, srv, "GET", "/fragments/backup-list", "", nil); status != http.StatusNotFound {
		t.Fatalf("disabled list: %d", status)
	}
	_, body, _ := do(t, srv, "GET", "/settings", "", nil)
	wantNotContains(t, body, "Stored backups", "/fragments/backup-list")

	state.Enabled = true
	_, body, _ = do(t, srv, "GET", "/settings", "", nil)
	wantContains(t, body, "Stored backups", `hx-get="/fragments/backup-list"`, `hx-trigger="load, data-changed from:body delay:2s"`)

	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "No backups in the bucket yet")
	wantNotContains(t, body, "<table", "<html")

	objects = []backup.Object{
		{Name: "20260903T030000.000000000Z-cccc.sqlite.gz", Size: 2_500_000, LastModified: time.Date(2026, 9, 3, 3, 0, 7, 0, time.UTC)},
		{Name: "20260902T030000.000000000Z-bbbb.sqlite.gz", Size: 1_250_000, LastModified: time.Date(2026, 9, 2, 3, 0, 5, 0, time.UTC)},
		{Name: "20260901T030000.000000000Z-aaaa.sqlite.gz", Size: 1_000_000, LastModified: time.Date(2026, 9, 1, 3, 0, 5, 0, time.UTC)},
	}
	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "3 backups, 4.8 MB in total", "2026-09-03 03:00 UTC", "2.5 MB", "1.2 MB",
		`href="/settings/backups/20260903T030000.000000000Z-cccc.sqlite.gz"`, "Show 2 older backups")
	newest, older := strings.Index(body, "cccc"), strings.Index(body, `<tbody id="backup-list-older" hidden>`)
	if newest < 0 || older < newest || strings.Index(body, "bbbb") < older || strings.Index(body, "aaaa") < strings.Index(body, "bbbb") {
		t.Fatal("only the newest backup should be shown before the collapsed older ones")
	}
	objects = objects[:2]
	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "Show 1 older backup<")
	objects = objects[:1]
	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "1 backup, 2.5 MB in total")
	wantNotContains(t, body, "backup-list-older", "data-backup-list-toggle")

	objects, listErr = nil, backup.ErrListDenied
	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "can’t list the bucket", "docs/backups.md#permissions")
	listErr = errors.New("connection refused")
	_, body, _ = do(t, srv, "GET", "/fragments/backup-list", "", nil)
	wantContains(t, body, "couldn’t be listed")
	wantNotContains(t, body, "connection refused")
}

func TestByteSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1_234_567: "1.2 MB", 999_960: "1.0 MB", 5_000_000_000: "5.0 GB", 2_000_000_000_000_000: "2000.0 TB"} {
		if got := byteSize(n); got != want {
			t.Errorf("byteSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBackupDownload(t *testing.T) {
	state := backup.Status{}
	var gotName string
	var downloadErr error
	srv := httptest.NewServer(New(seedtest.DB(t), nil, nil, nil, "test",
		WithBackupStatus(func() backup.Status { return state }),
		WithBackupFiles(nil, func(_ context.Context, name string) (io.ReadCloser, int64, error) {
			gotName = name
			if downloadErr != nil {
				return nil, 0, downloadErr
			}
			return io.NopCloser(strings.NewReader("archive")), 7, nil
		})))
	defer srv.Close()
	const path = "/settings/backups/20260901T030000.000000000Z-aaaa.sqlite.gz"

	if status, _, _ := do(t, srv, "GET", path, "", nil); status != http.StatusNotFound || gotName != "" {
		t.Fatalf("disabled download: %d", status)
	}
	state.Enabled = true
	if status, _, resp := do(t, srv, "HEAD", path, "", nil); status != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET" || gotName != "" {
		t.Fatalf("HEAD: %d, Allow=%q, name=%q", status, resp.Header.Get("Allow"), gotName)
	}
	status, body, resp := do(t, srv, "GET", path, "", nil)
	if status != http.StatusOK || body != "archive" || gotName != "20260901T030000.000000000Z-aaaa.sqlite.gz" {
		t.Fatalf("download: %d %q name=%q", status, body, gotName)
	}
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename=20260901T030000.000000000Z-aaaa.sqlite.gz` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if resp.Header.Get("Content-Type") != "application/gzip" || resp.Header.Get("Content-Length") != "7" {
		t.Fatalf("headers: %v", resp.Header)
	}
	for err, want := range map[error]int{backup.ErrNotFound: http.StatusNotFound, backup.ErrDownloadDenied: http.StatusForbidden, errors.New("connection refused"): http.StatusBadGateway} {
		downloadErr = err
		status, body, _ := do(t, srv, "GET", path, "", nil)
		if status != want || strings.Contains(body, "connection refused") {
			t.Errorf("%v: %d %q", err, status, body)
		}
	}
}

type endlessBackup struct{ closed chan struct{} }

func (endlessBackup) Read(p []byte) (int, error) { return len(p), nil }
func (b endlessBackup) Close() error             { close(b.closed); return nil }

// A client that stops reading blocks the response write, which cancelling the
// context alone does not interrupt.
func TestBackupDownloadStalledClientShutdown(t *testing.T) {
	hub := live.New()
	backupBody := endlessBackup{closed: make(chan struct{})}
	srv := httptest.NewServer(New(seedtest.DB(t), nil, nil, hub, "test",
		WithBackupStatus(func() backup.Status { return backup.Status{Enabled: true} }),
		WithBackupFiles(nil, func(context.Context, string) (io.ReadCloser, int64, error) { return backupBody, -1, nil })))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /settings/backups/20260901T030000.000000000Z-aaaa.sqlite.gz HTTP/1.1\r\nHost: %s\r\n\r\n", srv.Listener.Addr())
	// Let the unread response fill the socket buffers.
	time.Sleep(300 * time.Millisecond)
	hub.InterruptStreams()
	select {
	case <-backupBody.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("download still blocked after shutdown began")
	}
}
