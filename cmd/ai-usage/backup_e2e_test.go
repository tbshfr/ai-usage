package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShutdownDuringBackupUpload(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ai-usage")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	entered := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer server.Close()
	defer close(released)
	t.Setenv("AI_USAGE_BACKUP_S3_BUCKET", "bucket")
	t.Setenv("AI_USAGE_BACKUP_S3_REGION", "auto")
	t.Setenv("AI_USAGE_BACKUP_S3_PREFIX", "test/")
	t.Setenv("AI_USAGE_BACKUP_S3_ENDPOINT", server.URL)
	t.Setenv("AI_USAGE_BACKUP_S3_ACCESS_KEY_ID", "test")
	t.Setenv("AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY", "test")
	dir := t.TempDir()
	port := freePort(t)
	cmd, logs := startApp(t, bin, dir, port, freePort(t))
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	waitReady(t, port)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("upload did not start")
	}
	start := time.Now()
	stopApp(t, cmd, logs)
	if elapsed := time.Since(start); elapsed >= shutdownGrace {
		t.Fatalf("shutdown took %s", elapsed)
	}
	assertShutdownLogs(t, logs.String())
	if strings.Contains(logs.String(), "backup succeeded") {
		t.Fatal("cancelled upload recorded success")
	}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), "attempt-") || entry.Name() == "success.json" {
			return fmt.Errorf("unexpected backup artifact %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackupStartupFailureCleanup(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ai-usage")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	var logs bytes.Buffer
	cmd := exec.Command(bin, "--http=", "--otlp-http=", "--otlp-grpc=invalid",
		"--data-dir="+dir, "--backup-s3-bucket=bucket", "--backup-s3-prefix=test/",
		"--backup-s3-region=auto", "--backup-s3-endpoint=http://127.0.0.1:1")
	// A token allows config validation to reach the listener startup failure.
	cmd.Env = append(os.Environ(), "AI_USAGE_OTLP_TOKEN=test", "AI_USAGE_BACKUP_S3_ACCESS_KEY_ID=test", "AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY=test")
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected startup error")
		}
	case <-time.After(shutdownGrace):
		cmd.Process.Kill()
		<-done
		t.Fatal("startup failure did not stop backup")
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.backups-*", "attempt-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("artifacts: %v, %v", entries, err)
	}
}
