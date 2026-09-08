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

type Worker struct {
	db                           *sql.DB
	client                       uploader
	bucket, prefix, version, dir string
	logger                       *slog.Logger
	now                          func() time.Time
	wait                         func(context.Context, time.Duration) bool
	snapshot                     func(context.Context, *sql.DB, string) error
	save                         func(success) error
}

func New(ctx context.Context, cfg *config.Config, db *sql.DB, version string, logger *slog.Logger) (*Worker, error) {
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
		dir: abs + ".backups-" + hex.EncodeToString(hash[:8]), logger: logger, now: time.Now, wait: wait, snapshot: storage.Snapshot}
	w.save = w.saveState
	return w, nil
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
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

func (w *Worker) saveState(s success) error {
	f, err := os.CreateTemp(w.dir, "state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(s); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(w.dir, "success.json"))
}

func dueDelay(s success, now time.Time) time.Duration {
	if s.SnapshotTime.IsZero() {
		return 0
	}
	return max(0, s.SnapshotTime.Add(interval).Sub(now))
}

func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("backups enabled", "interval", interval.String())
	var last success
	retry := time.Minute
	var warned time.Time
	prepared := false
	for ctx.Err() == nil {
		if !prepared {
			if err := w.prepare(); err != nil {
				w.logger.Error("backup failed", "stage", "prepare")
				w.warnStale(last, &warned)
				if !w.wait(ctx, retryDelay(retry)) {
					return
				}
				retry = min(retry*2, time.Hour)
				continue
			}
			prepared = true
			last = w.loadState()
			if !last.SnapshotTime.IsZero() {
				w.logger.Info("backup last success", "snapshot_time", last.SnapshotTime, "completion_time", last.CompletionTime, "object_key", last.ObjectKey)
			}
		}
		if !w.wait(ctx, dueDelay(last, w.now())) {
			return
		}
		started := w.now()
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		result, size, stage, err := w.attempt(attemptCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// SDK errors can include signed URLs and credential-provider output.
			w.logger.Error("backup failed", "stage", stage, "elapsed", w.now().Sub(started), "compressed_bytes", size, "last_successful_snapshot_time", last.SnapshotTime)
			w.warnStale(last, &warned)
			if !w.wait(ctx, retryDelay(retry)) {
				return
			}
			retry = min(retry*2, time.Hour)
			continue
		}
		last = result
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
