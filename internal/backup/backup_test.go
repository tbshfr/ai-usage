package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/tbshfr/ai-usage/internal/config"
	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/storage"
)

type uploadFunc func(context.Context, *s3.PutObjectInput) error

func (f uploadFunc) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, f(ctx, in)
}

func testWorker(t *testing.T) *Worker {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO generations(id,timestamp,source,input_tokens,output_tokens,cost,created_at) VALUES('record',1,'opencode',100,50,0.02,1)"); err != nil {
		t.Fatal(err)
	}
	w := &Worker{db: db, dir: filepath.Join(t.TempDir(), "backups"), bucket: "bucket", prefix: "home/", version: "test",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now, wait: wait, snapshot: storage.Snapshot,
		client: uploadFunc(func(context.Context, *s3.PutObjectInput) error { return nil })}
	w.save = w.saveState
	if err := w.prepare(); err != nil {
		t.Fatal(err)
	}
	return w
}

func noArtifacts(t *testing.T, w *Worker) {
	t.Helper()
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "success.json" {
			t.Errorf("artifact leaked: %s", entry.Name())
		}
	}
}

func TestS3RoundTripAndRetry(t *testing.T) {
	w := testWorker(t)
	var mu sync.Mutex
	var uploaded []byte
	var objectKey, checksum string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			rw.Write(uploaded)
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("unexpected request %s", r.Method)
			rw.WriteHeader(405)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256(body)
		got := base64.StdEncoding.EncodeToString(sum[:])
		if r.Header.Get("X-Amz-Checksum-Sha256") != got {
			t.Errorf("missing or wrong checksum")
		}
		if r.ContentLength != int64(len(body)) {
			t.Errorf("length %d != %d", r.ContentLength, len(body))
		}
		if r.Header.Get("X-Amz-Meta-Application-Version") != "test" || r.Header.Get("X-Amz-Meta-Snapshot-Time") == "" {
			t.Error("missing metadata")
		}
		if r.Header.Get("X-Amz-Storage-Class") != "STANDARD" {
			t.Error("wrong storage class")
		}
		if calls > 0 && (objectKey != r.URL.Path || !bytes.Equal(uploaded, body)) {
			t.Error("retry changed candidate/key")
		}
		objectKey = r.URL.Path
		uploaded = body
		checksum = got
		calls++
		if calls == 1 {
			rw.WriteHeader(503)
			rw.Write([]byte("<Error><Code>SlowDown</Code></Error>"))
			return
		}
		rw.Header().Set("X-Amz-Checksum-Sha256", got)
		rw.Header().Set("ETag", "\"test\"")
	}))
	defer server.Close()
	cfg := &config.Config{DatabasePath: filepath.Join(t.TempDir(), "db"), BackupS3Bucket: "bucket", BackupS3Prefix: "home/", BackupS3Region: "auto", BackupS3Endpoint: server.URL, BackupS3AccessKeyID: "test", BackupS3SecretAccessKey: "test"}
	real, err := New(context.Background(), cfg, w.db, "test", w.logger)
	if err != nil {
		t.Fatal(err)
	}
	w.client = real.client
	result, size, stage, err := w.attempt(context.Background())
	if err != nil {
		t.Fatalf("%s: %v", stage, err)
	}
	if size == 0 || w.loadState() != result {
		t.Fatal("success not persisted")
	}
	noArtifacts(t, w)
	response, err := http.Get(server.URL + objectKey)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(downloaded)
	if base64.StdEncoding.EncodeToString(sum[:]) != checksum {
		t.Fatal("download checksum differs")
	}
	gz, err := gzip.NewReader(bytes.NewReader(downloaded))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	gz.Close()
	path := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.ValidateSnapshot(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, tokens int
	var cost float64
	if err := db.QueryRow("SELECT count(*),sum(input_tokens+output_tokens),sum(cost) FROM generations").Scan(&count, &tokens, &cost); err != nil {
		t.Fatal(err)
	}
	if count != 1 || tokens != 150 || cost != 0.02 {
		t.Fatalf("restored %d %d %f", count, tokens, cost)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestAttemptFailures(t *testing.T) {
	for _, stage := range []string{"snapshot", "compression", "upload", "state", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			w := testWorker(t)
			calls := 0
			w.client = uploadFunc(func(context.Context, *s3.PutObjectInput) error {
				calls++
				if stage == "upload" {
					return errors.New("failed")
				}
				return nil
			})
			switch stage {
			case "snapshot":
				w.snapshot = func(ctx context.Context, db *sql.DB, path string) error {
					if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
						return err
					}
					return storage.ValidateSnapshot(ctx, path)
				}
			case "compression":
				w.snapshot = func(_ context.Context, _ *sql.DB, path string) error { return os.Mkdir(path, 0700) }
			case "state":
				w.save = func(success) error { return errors.New("disk full") }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "cancelled" {
				cancel()
			}
			_, _, _, err := w.attempt(ctx)
			if err == nil {
				t.Fatal("false success")
			}
			if !w.loadState().SnapshotTime.IsZero() {
				t.Fatal("advanced schedule")
			}
			if (stage == "snapshot" || stage == "compression" || stage == "cancelled") && calls != 0 {
				t.Fatal("uploaded invalid candidate")
			}
			noArtifacts(t, w)
		})
	}
}

func TestCompressErrors(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, bytes.Repeat([]byte("data"), 1000), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := compress(ctx, source, filepath.Join(dir, "cancel")); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, _, err := compress(context.Background(), source, dir); err == nil {
		t.Fatal("output failure ignored")
	}
}

func TestSchedule(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		age, delay time.Duration
		empty      bool
	}{
		{"first", 0, 0, true}, {"before due", time.Hour, 23 * time.Hour, false},
		{"due", interval, 0, false}, {"long downtime", 10 * interval, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testWorker(t)
			current := now
			w.now = func() time.Time { return current }
			if !tc.empty {
				if err := w.saveState(success{SnapshotTime: now.Add(-tc.age), CompletionTime: now.Add(-tc.age), ObjectKey: "home/previous.sqlite.gz"}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			waits := 0
			w.client = uploadFunc(func(context.Context, *s3.PutObjectInput) error { calls++; return nil })
			w.wait = func(_ context.Context, d time.Duration) bool {
				waits++
				if waits == 1 {
					if d != tc.delay {
						t.Errorf("delay=%s want %s", d, tc.delay)
					}
					current = current.Add(d)
					return true
				}
				if d != interval {
					t.Errorf("next delay=%s", d)
				}
				return false
			}
			w.Run(context.Background())
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestScheduleFailureAndCancellation(t *testing.T) {
	w := testWorker(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	var logs bytes.Buffer
	w.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	calls := 0
	waits := 0
	w.client = uploadFunc(func(context.Context, *s3.PutObjectInput) error {
		calls++
		return errors.New("https://secret:credential@example.com?signed=secret")
	})
	w.wait = func(_ context.Context, d time.Duration) bool {
		waits++
		if waits%2 == 0 {
			base := time.Minute * time.Duration(1<<(calls-1))
			if d < base/2 || d > base {
				t.Errorf("retry=%s", d)
			}
			now = now.Add(d)
		}
		return calls < 3
	}
	w.Run(context.Background())
	if !w.loadState().SnapshotTime.IsZero() {
		t.Fatal("failure advanced schedule")
	}
	if strings.Count(logs.String(), "backup stale") != 1 {
		t.Fatalf("warnings: %s", logs.String())
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("credential leaked")
	}
	noArtifacts(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	w.wait = wait
	w.client = uploadFunc(func(ctx context.Context, _ *s3.PutObjectInput) error { close(entered); <-ctx.Done(); return ctx.Err() })
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upload not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
	noArtifacts(t, w)
}

func TestStateAndCleanup(t *testing.T) {
	w := testWorker(t)
	for _, body := range []string{"broken", `{}`, `{"snapshot_time":"2999-01-01T00:00:00Z"}`} {
		if err := os.WriteFile(filepath.Join(w.dir, "success.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if !w.loadState().SnapshotTime.IsZero() {
			t.Fatal("accepted invalid state")
		}
	}
	if err := os.Mkdir(filepath.Join(w.dir, "attempt-stale"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, "unrelated"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "attempt-stale")); !os.IsNotExist(err) {
		t.Fatal("stale artifact not removed")
	}
	if _, err := os.Stat(filepath.Join(w.dir, "unrelated")); err != nil {
		t.Fatal("unrelated file removed")
	}
}

func TestDisabledDoesNotLoadAWS(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "malformed"))
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := New(context.Background(), &config.Config{}, nil, "test", nil)
	if err != nil || w != nil {
		t.Fatalf("disabled: %v %v", w, err)
	}
}

func TestRegionAndDestinationScope(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_REGION", "unused-region")
	t.Setenv("AWS_DEFAULT_REGION", "unused-default-region")
	t.Setenv("S3_REGION", "ignored")
	t.Setenv("S3_DEFAULT_REGION", "ignored")
	cfg := &config.Config{DatabasePath: filepath.Join(t.TempDir(), "db"), BackupS3Bucket: "bucket", BackupS3Prefix: "home/"}
	if _, err := New(context.Background(), cfg, nil, "test", nil); err == nil {
		t.Fatal("missing region accepted")
	}
	cfg.BackupS3Region = "auto"
	a, err := New(context.Background(), cfg, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupS3Prefix = "other/"
	b, err := New(context.Background(), cfg, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.dir == b.dir {
		t.Fatal("destination shares state")
	}
}

func TestConfiguredCredentials(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "malformed"))
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "unused-profile")
	t.Setenv("AWS_ACCESS_KEY_ID", "unused-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unused-secret")
	t.Setenv("AWS_SESSION_TOKEN", "unused-session")
	t.Setenv("S3_ACCESS_KEY_ID", "environment-key")
	t.Setenv("S3_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("S3_SESSION_TOKEN", "environment-session")
	cfg := &config.Config{DatabasePath: filepath.Join(t.TempDir(), "db"), BackupS3Bucket: "bucket", BackupS3Prefix: "home/", BackupS3Region: "auto"}
	cfg.BackupS3AccessKeyID = "configured-key"
	cfg.BackupS3SecretAccessKey = "configured-secret"
	cfg.BackupS3SessionToken = "configured-session"
	w, err := New(context.Background(), cfg, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.client.(*s3.Client).Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "configured-key" || got.SecretAccessKey != "configured-secret" || got.SessionToken != "configured-session" {
		t.Fatal("configured credentials were not used")
	}
	for _, missing := range []string{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"} {
		t.Run(missing, func(t *testing.T) {
			cfg := *cfg
			if missing == "S3_ACCESS_KEY_ID" {
				cfg.BackupS3AccessKeyID = ""
			} else {
				cfg.BackupS3SecretAccessKey = ""
			}
			w, err := New(context.Background(), &cfg, nil, "test", nil)
			if err != nil {
				t.Fatalf("missing credentials blocked startup: %v", err)
			}
			if _, err := w.client.(*s3.Client).Options().Credentials.Retrieve(context.Background()); err == nil {
				t.Fatal("missing credentials accepted")
			}
		})
	}
}

func TestConfiguredRegionIgnoresEnvironment(t *testing.T) {
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_DEFAULT_REGION", "us-west-2")
	cfg := &config.Config{DatabasePath: filepath.Join(t.TempDir(), "db"), BackupS3Bucket: "bucket", BackupS3Prefix: "home/", BackupS3Region: "auto"}
	w, err := New(context.Background(), cfg, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.client.(*s3.Client).Options().Region; got != "auto" {
		t.Fatalf("region = %q, want auto", got)
	}
}

func TestRunStatusFailureAndRecovery(t *testing.T) {
	w := testWorker(t)
	var notifications []Status
	w.notify = func() { notifications = append(notifications, w.Status()) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	w.client = uploadFunc(func(context.Context, *s3.PutObjectInput) error {
		calls++
		if !w.Status().Running {
			t.Error("attempt not marked running")
		}
		if calls == 1 {
			return errors.New("secret error must not be exposed")
		}
		if w.Status().FailureStage != "upload" {
			t.Error("failure cleared before recovery")
		}
		return nil
	})
	w.wait = func(context.Context, time.Duration) bool {
		s := w.Status()
		if calls == 1 && s.FailureStage != "upload" {
			t.Fatal("missing upload failure")
		}
		if calls == 2 {
			if s.Running || s.FailureStage != "" || !s.FailedAt.IsZero() || s.LastSuccess.IsZero() {
				t.Fatalf("bad recovered status: %+v", s)
			}
			cancel()
			return false
		}
		return true
	}
	w.Run(ctx)
	if calls != 2 {
		t.Fatalf("attempts = %d", calls)
	}
	// Preparation, start/failure, and start/success each publish one state.
	if len(notifications) != 5 {
		t.Fatalf("notifications = %d, want 5: %+v", len(notifications), notifications)
	}
	if notifications[2].Running || notifications[2].FailureStage != "upload" {
		t.Fatalf("failure notification is incomplete: %+v", notifications[2])
	}
	if notifications[4].Running || notifications[4].FailureStage != "" || notifications[4].LastSuccess.IsZero() {
		t.Fatalf("success notification is incomplete: %+v", notifications[4])
	}
	var disabled *Worker
	if disabled.Status().Enabled {
		t.Fatal("nil worker enabled")
	}
}

func TestStatusChangesNotifySSEHub(t *testing.T) {
	w := testWorker(t)
	hub := live.New()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	w.notify = func() {
		// Reading status in the callback must not deadlock.
		_ = w.Status()
		hub.Notify()
	}
	for _, update := range []func(*Status){
		func(s *Status) { s.Running = true },
		func(s *Status) { s.Running = false; s.FailureStage = "upload" },
		func(s *Status) { s.FailureStage = ""; s.LastSuccess = time.Now() },
	} {
		w.updateStatus(update)
		select {
		case <-events:
		default:
			t.Fatal("backup status change did not notify SSE subscribers")
		}
	}
}

func TestPrepareRecoveryClearsFailureBeforeScheduledBackup(t *testing.T) {
	w := testWorker(t)
	now := time.Now().UTC()
	w.now = func() time.Time { return now }
	last := success{SnapshotTime: now.Add(-time.Hour), CompletionTime: now.Add(-time.Hour), ObjectKey: w.prefix + "saved.sqlite.gz"}
	if err := w.saveState(last); err != nil {
		t.Fatal(err)
	}
	healthyDir := w.dir
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	w.dir = blocked
	waits := 0
	w.wait = func(_ context.Context, delay time.Duration) bool {
		waits++
		s := w.Status()
		switch waits {
		case 1:
			if s.FailureStage != "prepare" || s.FailedAt.IsZero() {
				t.Fatalf("missing prepare failure: %+v", s)
			}
			w.dir = healthyDir
			return true
		case 2:
			if s.FailureStage != "" || !s.FailedAt.IsZero() || !s.LastSuccess.Equal(last.CompletionTime) {
				t.Fatalf("stale status after preparation recovered: %+v", s)
			}
			if delay != 23*time.Hour {
				t.Fatalf("scheduled delay = %v", delay)
			}
			return false
		default:
			t.Fatal("unexpected wait")
			return false
		}
	}
	w.Run(context.Background())
	if waits != 2 {
		t.Fatalf("waits = %d", waits)
	}
}
