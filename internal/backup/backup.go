package backup

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/tbshfr/ai-usage/internal/config"
	"github.com/tbshfr/ai-usage/internal/storage"
)

const interval = 24 * time.Hour
const attemptTimeout = 30 * time.Minute
const maxObjectBytes = 5_000_000_000

type uploader interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type success struct {
	SnapshotTime   time.Time `json:"snapshot_time"`
	CompletionTime time.Time `json:"completion_time"`
	ObjectKey      string    `json:"object_key"`
}

type scheduleState struct {
	ChangedAt time.Time `json:"changed_at"`
}

// Status is a credential-free snapshot of backup health.
type Status struct {
	Enabled      bool
	Running      bool
	Requested    bool
	LastSuccess  time.Time
	NextRun      time.Time
	FailedAt     time.Time
	FailureStage string
}

// Status returns the current state; a nil worker means backups are disabled.
func (w *Worker) Status() Status {
	if w == nil {
		return Status{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := w.status
	s.Enabled = true
	return s
}

func (w *Worker) updateStatus(fn func(*Status)) {
	w.setStatus(fn)
	if w.notify != nil {
		w.notify()
	}
}

// The transition these details accompany has already notified, so another
// notification would only refresh open pages twice.
func (w *Worker) setStatus(fn func(*Status)) {
	w.mu.Lock()
	fn(&w.status)
	w.mu.Unlock()
}

func (w *Worker) Start() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	if w.status.Running || w.status.Requested {
		w.mu.Unlock()
		return false
	}
	w.status.Requested = true
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	w.wakeUp()
	return true
}

// The change is saved beside success.json so that after a restart an earlier
// time of day is not mistaken for a missed backup.
func (w *Worker) Reschedule() {
	if w == nil {
		return
	}
	w.rescheduleMu.Lock()
	defer w.rescheduleMu.Unlock()
	now := w.now().UTC()
	w.mu.Lock()
	w.rescheduled = now
	w.mu.Unlock()
	if err := w.writeState("schedule.json", scheduleState{ChangedAt: now}); err != nil {
		w.logger.Warn("backup schedule change not saved")
	}
	w.wakeUp()
}

func (w *Worker) wakeUp() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) requested() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status.Requested
}

type Worker struct {
	notify                       func()
	mu                           sync.RWMutex
	status                       Status
	rescheduled                  time.Time
	rescheduleMu                 sync.Mutex // orders schedule.json writes
	wake                         chan struct{}
	woken                        bool // not guarded by mu: only Run's goroutine uses it
	db                           *sql.DB
	client                       uploader
	bucket, prefix, version, dir string
	logger                       *slog.Logger
	now                          func() time.Time
	wait                         func(context.Context, time.Duration) bool
	snapshot                     func(context.Context, *sql.DB, string) error
	save                         func(success) error
}

func New(ctx context.Context, cfg *config.Config, db *sql.DB, version string, logger *slog.Logger, notify ...func()) (*Worker, error) {
	if cfg.BackupS3Bucket == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	region := cfg.BackupS3Region
	if strings.TrimSpace(region) == "" {
		return nil, errors.New("backup region required: set --backup-s3-region or AI_USAGE_BACKUP_S3_REGION (auto for R2)")
	}
	ac := aws.Config{
		Region: region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			cfg.BackupS3AccessKeyID,
			cfg.BackupS3SecretAccessKey,
			cfg.BackupS3SessionToken,
		)),
	}
	client := s3.NewFromConfig(ac, func(o *s3.Options) {
		if cfg.BackupS3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.BackupS3Endpoint)
			o.UsePathStyle = true
		}
		o.RetryMaxAttempts = 3
	})
	abs, err := filepath.Abs(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	identity, _ := json.Marshal([]string{abs, cfg.BackupS3Bucket, ac.Region, cfg.BackupS3Prefix, cfg.BackupS3Endpoint})
	hash := sha256.Sum256(identity)
	w := &Worker{db: db, client: client, bucket: cfg.BackupS3Bucket, prefix: cfg.BackupS3Prefix, version: version,
		dir: abs + ".backups-" + hex.EncodeToString(hash[:8]), logger: logger, now: time.Now, snapshot: storage.Snapshot,
		wake: make(chan struct{}, 1)}
	w.wait = w.sleep
	if len(notify) > 0 {
		w.notify = notify[0]
	}
	w.save = w.saveState
	return w, nil
}

func (w *Worker) sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.wake:
		w.woken = true
		return ctx.Err() == nil
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// The schedule is resolved before a manual request is honored, so the default
// time is saved from the existing cycle rather than from the manual backup.
func (w *Worker) waitDue(ctx context.Context, last success) bool {
	for rescheduled := false; ; {
		next := nextRun(last, w.schedule(ctx, last), w.rescheduledAt(), w.now())
		if w.requested() {
			return ctx.Err() == nil
		}
		if rescheduled {
			w.updateStatus(func(s *Status) { s.NextRun = next })
		} else {
			w.setStatus(func(s *Status) { s.NextRun = next })
		}
		w.woken = false
		if !w.wait(ctx, max(0, next.Sub(w.now()))) {
			return false
		}
		// The timer runs on the monotonic clock, so a wall clock set back
		// during the wait fires it early. Backing up then would count the
		// next run from before the scheduled time and back up again at it.
		if !w.woken && !w.now().Before(next) {
			return true
		}
		rescheduled = w.woken
	}
}

// Start and Reschedule share the wake channel, but only a manual request may
// end a retry delay early.
func (w *Worker) backoff(ctx context.Context, delay time.Duration) bool {
	deadline := w.now().Add(delay)
	w.setStatus(func(s *Status) { s.NextRun = deadline })
	for {
		w.woken = false
		if !w.wait(ctx, delay) {
			return false
		}
		if delay = deadline.Sub(w.now()); !w.woken || delay <= 0 || w.requested() {
			return true
		}
	}
}

func (w *Worker) rescheduledAt() time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.rescheduled
}

type daily struct {
	hour, minute int
	loc          *time.Location
}

func parseDaily(s storage.BackupSettings) (daily, bool) {
	clock, err := time.Parse("15:04", s.Time)
	if err != nil {
		return daily{}, false
	}
	loc, err := s.Location()
	if err != nil {
		return daily{}, false
	}
	return daily{clock.Hour(), clock.Minute(), loc}, true
}

// Until a time is chosen, backups keep the time of day of the first one.
// Using the last snapshot rather than now means upgrading from the old
// 24-hour interval does not move an existing cycle.
func (w *Worker) schedule(ctx context.Context, last success) daily {
	settings, err := storage.ReadDashboardSettings(ctx, w.db)
	if err == nil {
		if d, ok := parseDaily(settings.Backup); ok {
			return d
		}
	}
	first := w.now()
	if !last.SnapshotTime.IsZero() {
		first = last.SnapshotTime
	}
	chosen := storage.BackupSettings{Time: first.UTC().Format("15:04"), Timezone: "UTC"}
	if err != nil {
		w.logger.Warn("backup schedule unavailable, using time of first backup")
	} else if body, _ := json.Marshal(chosen); storage.SaveDashboardSettings(ctx, w.db, "backup", body) != nil {
		w.logger.Warn("backup schedule not saved")
	}
	d, _ := parseDaily(chosen)
	return d
}

func (w *Worker) prepare() error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(w.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup directory must be a real directory")
	}
	if err := os.Chmod(w.dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "attempt-") || strings.HasPrefix(entry.Name(), "state-") {
			if err := os.RemoveAll(filepath.Join(w.dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Worker) loadState() success {
	var s success
	body, err := os.ReadFile(filepath.Join(w.dir, "success.json"))
	if err != nil || json.Unmarshal(body, &s) != nil {
		return success{}
	}
	now := w.now()
	if s.SnapshotTime.IsZero() || s.SnapshotTime.After(now) || s.CompletionTime.Before(s.SnapshotTime) || s.CompletionTime.After(now) || !strings.HasPrefix(s.ObjectKey, w.prefix) || !strings.HasSuffix(s.ObjectKey, ".sqlite.gz") {
		return success{}
	}
	return s
}

// A schedule change time in the future is ignored: it could only come from a
// clock set back since, and would postpone backups until that time passed.
func (w *Worker) loadRescheduled() {
	var s scheduleState
	body, err := os.ReadFile(filepath.Join(w.dir, "schedule.json"))
	if err != nil || json.Unmarshal(body, &s) != nil || s.ChangedAt.After(w.now()) {
		return
	}
	w.mu.Lock()
	if s.ChangedAt.After(w.rescheduled) {
		w.rescheduled = s.ChangedAt
	}
	w.mu.Unlock()
}

func (w *Worker) saveState(s success) error {
	return w.writeState("success.json", s)
}

func (w *Worker) writeState(name string, v any) error {
	f, err := os.CreateTemp(w.dir, "state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(v); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(w.dir, name))
}

// Counting from the previous snapshot runs a time missed during downtime at
// startup. Counting from the last schedule change as well keeps a change to an
// earlier time of day from backing up at once.
func nextRun(s success, d daily, rescheduled, now time.Time) time.Time {
	if s.SnapshotTime.IsZero() {
		return now
	}
	from := s.SnapshotTime
	if rescheduled.After(from) {
		from = rescheduled
	}
	local := from.In(d.loc)
	for day := 0; ; day++ {
		// A time skipped by a DST change resolves to one of the day's offsets,
		// so the backup still runs once that day.
		at := time.Date(local.Year(), local.Month(), local.Day()+day, d.hour, d.minute, 0, 0, d.loc)
		if at.After(from) {
			return at
		}
	}
}

func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("backups enabled")
	var last success
	retry := time.Minute
	var warned time.Time
	prepared := false
	for ctx.Err() == nil {
		if !prepared {
			if err := w.prepare(); err != nil {
				w.updateStatus(func(s *Status) { s.Requested = false; s.FailedAt = w.now(); s.FailureStage = "prepare" })
				w.logger.Error("backup failed", "stage", "prepare")
				w.warnStale(last, &warned)
				if !w.backoff(ctx, retryDelay(retry)) {
					return
				}
				retry = min(retry*2, time.Hour)
				continue
			}
			prepared = true
			last = w.loadState()
			w.loadRescheduled()
			w.updateStatus(func(s *Status) {
				s.LastSuccess = last.CompletionTime
				s.FailureStage = ""
				s.FailedAt = time.Time{}
			})
			if !last.SnapshotTime.IsZero() {
				w.logger.Info("backup last success", "snapshot_time", last.SnapshotTime, "completion_time", last.CompletionTime, "object_key", last.ObjectKey)
			}
		}
		if !w.waitDue(ctx, last) {
			return
		}
		w.updateStatus(func(s *Status) { s.Running = true; s.Requested = false; s.NextRun = time.Time{} })
		started := w.now()
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		result, size, stage, err := w.attempt(attemptCtx)
		cancel()
		if ctx.Err() != nil {
			w.updateStatus(func(s *Status) { s.Running = false })
			return
		}
		if err != nil {
			w.updateStatus(func(s *Status) { s.Running = false; s.FailedAt = w.now(); s.FailureStage = stage })
			// SDK errors can include signed URLs and credential-provider output.
			w.logger.Error("backup failed", "stage", stage, "elapsed", w.now().Sub(started), "compressed_bytes", size, "last_successful_snapshot_time", last.SnapshotTime)
			w.warnStale(last, &warned)
			if !w.backoff(ctx, retryDelay(retry)) {
				return
			}
			retry = min(retry*2, time.Hour)
			continue
		}
		last = result
		w.updateStatus(func(s *Status) {
			s.Running = false
			s.LastSuccess = result.CompletionTime
			s.FailedAt = time.Time{}
			s.FailureStage = ""
		})
		retry = time.Minute
		w.logger.Info("backup succeeded", "snapshot_time", last.SnapshotTime, "completion_time", last.CompletionTime, "object_key", last.ObjectKey, "elapsed", w.now().Sub(started), "compressed_bytes", size)
	}
}

func retryDelay(base time.Duration) time.Duration {
	return base/2 + time.Duration(mathrand.Int64N(int64(base/2)+1))
}

func (w *Worker) warnStale(last success, warned *time.Time) {
	now := w.now()
	if (last.SnapshotTime.IsZero() || now.Sub(last.SnapshotTime) >= interval) && (warned.IsZero() || now.Sub(*warned) >= time.Hour) {
		w.logger.Warn("backup stale", "last_successful_snapshot_time", last.SnapshotTime)
		*warned = now
	}
}

func (w *Worker) attempt(ctx context.Context) (success, int64, string, error) {
	var result success
	dir, err := os.MkdirTemp(w.dir, "attempt-")
	if err != nil {
		return result, 0, "temp", err
	}
	defer os.RemoveAll(dir)
	result.SnapshotTime = w.now().UTC()
	snapshot := filepath.Join(dir, "snapshot.sqlite")
	if err := w.snapshot(ctx, w.db, snapshot); err != nil {
		return result, 0, "snapshot", err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return result, 0, "key", err
	}
	result.ObjectKey = w.prefix + result.SnapshotTime.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:]) + ".sqlite.gz"
	compressed := filepath.Join(dir, "snapshot.sqlite.gz")
	checksum, size, err := compress(ctx, snapshot, compressed)
	if err != nil {
		return result, size, "compression", err
	}
	if size > maxObjectBytes {
		return result, size, "size_limit", errors.New("compressed backup exceeds single PUT limit")
	}
	file, err := os.Open(compressed)
	if err != nil {
		return result, size, "upload_file", err
	}
	defer file.Close()
	_, err = w.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(w.bucket), Key: aws.String(result.ObjectKey), Body: file, ContentLength: aws.Int64(size),
		ContentType: aws.String("application/gzip"), StorageClass: types.StorageClassStandard,
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(checksum),
		Metadata: map[string]string{"snapshot-time": result.SnapshotTime.Format(time.RFC3339Nano), "application-version": w.version, "sha256": checksum},
	})
	if err != nil {
		return result, size, "upload", err
	}
	if err := ctx.Err(); err != nil {
		return result, size, "cancelled", err
	}
	result.CompletionTime = w.now().UTC()
	if err := w.save(result); err != nil {
		return result, size, "state", err
	}
	return result, size, "", nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func compress(ctx context.Context, source, dest string) (string, int64, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(output, hash))
	_, copyErr := io.Copy(gz, contextReader{ctx, input})
	gzipErr := gz.Close()
	closeErr := output.Close()
	if err := errors.Join(copyErr, gzipErr, closeErr, ctx.Err()); err != nil {
		return "", 0, err
	}
	info, err := os.Stat(dest)
	if err != nil {
		return "", 0, fmt.Errorf("stat compressed backup: %w", err)
	}
	return base64.StdEncoding.EncodeToString(hash.Sum(nil)), info.Size(), nil
}
