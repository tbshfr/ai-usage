# Phase 11 — Daily SQLite backups to S3

Read `docs/plans/README.md` first. Prerequisite: Phases 1–10 merged.
Status: implemented; local verification passed. Operator R2 smoke test pending
bucket/credentials (see docs/backups.md).

## Goal and decisions

Add optional daily backups inside the existing Go binary: create a consistent
SQLite snapshot with `VACUUM INTO`, gzip it, and upload it to S3. Restore by
downloading and decompressing a single object. Expire old backups through an
S3 Lifecycle rule scoped to the backup prefix.

- Use `VACUUM INTO` through `database/sql`. It produces a compact standalone
  database, removes unused space, and avoids driver-specific backup plumbing.
  Never copy the live `.db` file directly: the application uses WAL.
- The online backup API uses less CPU and supports incremental page copying,
  but still produces a full backup. Reconsider it only if measurements show
  unacceptable snapshot overhead. `VACUUM INTO` leaves the source unchanged;
  concurrent WAL writers can continue, although CPU/I/O contention and WAL
  growth during the read must be considered.
- Do not embed Litestream in this phase. Daily snapshots meet the selected
  recovery model without adding replication lifecycle management.
- With successful daily runs, recovery can lose up to approximately 24 hours
  of committed data; failures and downtime can extend that window.
- Retention is an operator-selected positive number of days, **X**, in the
  bucket lifecycle configuration. Use **7 days as the documented starting
  recommendation**, not a confirmed user requirement. No app-side deletion
  loop or misleading application retention flag.
- Approximate remote storage is X times the compressed snapshot size, plus
  expiration delay. This limits backup history, not growth of the database.

## Work items

### 1. Configuration (`internal/config`)

Follow existing flag > environment > default precedence:

| Flag | Environment variable | Default |
|------|----------------------|---------|
| `--backup-s3-bucket` | `AI_USAGE_BACKUP_S3_BUCKET` | empty; backups disabled |
| `--backup-s3-region` | `AI_USAGE_BACKUP_S3_REGION` | S3_REGION, then S3_DEFAULT_REGION |
| `--backup-s3-prefix` | `AI_USAGE_BACKUP_S3_PREFIX` | required when enabled; e.g. `ai-usage/home/` |
| `--backup-s3-endpoint` | `AI_USAGE_BACKUP_S3_ENDPOINT` | AWS S3 endpoint |

- Require a nonempty dedicated prefix per database/instance and a resolved
  region when enabled. Validate endpoint syntax and reject embedded credentials.
  Reject explicitly supplied backup options without a bucket.
- Use environment credentials through the AWS SDK for Go v2 static credentials
  provider (S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, optional S3_SESSION_TOKEN).
  This replaces the original default credential-chain requirement following the
  user's R2-focused dependency simplification. No shared profiles, SSO, or role
  discovery. Do not add application credential flags or log secrets.
- Optional custom endpoints support S3-compatible deployments; document any
  required addressing configuration and verify the supported provider behavior.
- Keep the first version daily with a fixed 24-hour interval; no cron parser.
  When disabled, do not initialize AWS clients or perform credential discovery.

### 2. Snapshot creation (`internal/storage`)

- Add a context-aware helper that executes `VACUUM INTO ?` with a bound output
  path on a connection with no active transaction or unfinished statements.
- Create the output within an application-owned temporary directory with
  restrictive permissions. The destination must be absent or empty.
- Open the completed snapshot separately and require `PRAGMA integrity_check`
  to return `ok`. Close the validation connection before compression.
- Confirm the schema does not depend on implicit ROWIDs remaining stable;
  vacuuming can change ROWIDs without an explicit `INTEGER PRIMARY KEY`.
- Any error or cancellation invalidates the candidate. Never upload partial
  output. Clean up only the temporary files owned by this backup attempt.
- Account for local free space for the snapshot and compressed artifact;
  use bounded memory and handle disk-full errors without interrupting ingestion.

### 3. Upload and scheduling (`internal/backup`, new)

- Use a small backup worker and narrow S3 client interface for testing; add
  only the necessary AWS SDK v2 modules, retaining CGO-free builds.
- Compress the validated snapshot to a temporary `.sqlite.gz` file, then upload
  it using a unique UTC timestamp plus random suffix under the configured prefix.
  Use S3 Standard storage. Include snapshot time and application version as
  metadata; use SDK-supported upload checksum validation.
- Upload a completed local file with known length and bounded memory. If using
  multipart upload, abort on failure and document lifecycle cleanup of abandoned
  multipart uploads. Never treat multipart initiation as backup success.
- Record success only after S3 confirms completion. Persist snapshot time,
  completion time and object key atomically in a small local state file scoped
  to the database and destination, separate from the backed-up database.
- On startup, run immediately if there is no valid success record or the latest
  snapshot is at least 24 hours old. Otherwise wait until it is due. After a long
  outage create one current snapshot, not one for every missed day.
- Allow one backup at a time. Retry transient failures with bounded exponential
  backoff and jitter, with a deadline per attempt. Schedule based on successful
  snapshot time so failed attempts never advance the daily schedule.
- Reuse a candidate/key for retries within one attempt. A crash after upload but
  before saving state may create an extra object on restart; lifecycle expiry
  bounds its lifetime. Do not require remote listing for normal scheduling.
- Remove local artifacts after success, failure or cancellation. On startup,
  clean stale artifacts only within the worker's explicitly owned temp directory.

### 4. Lifecycle wiring and visibility (`cmd/ai-usage/main.go`)

- Start the worker after database migration; run it independently of request
  handling. Cancel and join it before closing the database on every exit path,
  including startup failures, within the existing shutdown grace period.
- Invalid local configuration fails startup. Remote outages, credential errors
  and failed backups are reported without taking ingestion or the dashboard down.
- Log enablement, attempt outcome, elapsed time, compressed bytes and last
  successful snapshot time. Never log credentials, signed URLs or database data.
- Emit a stale-backup warning when no successful snapshot exists after an
  attempted backup, or its age exceeds 24 hours; rate-limit repeated warnings.
  Explain how operators can monitor these logs. Do not make S3 availability
  a dependency of `/health` or `/ready`.

### 5. Retention and restore documentation

Add `docs/backups.md`, link it from `README.md`, and update the configuration
table, `.env.example` and deployment examples as appropriate.

- Provide an S3 Lifecycle example scoped to the exact instance prefix, with
  `Expiration.Days: 7` and clear instructions to replace 7 with X. Merge into
  existing bucket rules; do not overwrite unrelated lifecycle configuration.
  The application neither creates the bucket nor changes lifecycle policy.
- Explain that expiration is asynchronous and not an exact storage cap. With
  bucket versioning enabled, also expire noncurrent versions and clean expired
  delete markers; current-version expiration alone does not reclaim old data.
- Include abandoned multipart upload cleanup if multipart is used. Document
  least-privilege upload permissions for the prefix, with separate restore and
  lifecycle-administration permissions. Normal application operation needs no
  object deletion permission.
- Strict age-based retention eventually removes every backup if uploads fail
  for longer than X days. State this explicitly alongside stale-backup monitoring.
- Restore procedure: stop the app; download/decompress into a staging directory;
  verify gzip/checksum and SQLite integrity; preserve the existing database and
  its WAL/SHM files together; install the restored database with correct ownership
  and permissions, without stale WAL/SHM companions; restart and verify records
  and dashboard totals. Use the same application version or a compatible newer
  version. Do not automatically restore or overwrite a database at startup.
- Update the README's unconditional “everything stays on your machine” and
  “no outbound network connections” claims: enabling backups sends the stored
  database contents to the selected S3 destination.

### 6. Verification

- Config tests: disabled defaults, precedence, incomplete/invalid settings and
  no AWS initialization when disabled.
- Real SQLite test in WAL mode: committed records still in WAL are present in
  the snapshot; concurrent writes do not produce an inconsistent backup; source
  remains usable. Verify schema, representative records and aggregate totals.
- Snapshot/upload failures: cancellation, corrupt/incomplete candidate,
  compression errors, failed upload and local state write failure. No false
  success and no leaked handles or attempt artifacts.
- Scheduler tests using a controllable clock: first run, restart before/after
  due time, long downtime, retries, overlap prevention and shutdown cancellation.
- Round trip through an HTTP S3 test server: upload, download, decompress, open
  with SQLite and verify application data. Exercise multipart cleanup if used.
- Document a deployment smoke test against the selected S3 provider, including
  lifecycle inspection and an actual restore into a separate directory. Do not
  expire or overwrite production data as part of verification.
- Measure snapshot duration, compressed size and ingestion latency against a
  representative database; record results and any resource limitations.

## Final gate

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .
```

## Acceptance criteria

- [x] Default operation remains local and performs no backup-related networking.
- [x] Enabled deployments upload a valid compressed snapshot daily and catch up
      after restart, with no overlapping jobs.
- [x] Backup failures do not stop ingestion or advance the success timestamp.
- [x] Restoring one downloaded object recovers a usable database with matching data.
- [x] Retention documentation removes backups older than the selected X days,
      accounting for asynchronous expiry and versioned buckets (R2 setup documented).
- [x] Last success and stale backups are observable; credentials are never logged.
- [x] Shutdown and failure paths clean up resources; CGO-free builds still work.

## References

- [SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html)
- [SQLite online backup API](https://www.sqlite.org/backup.html)
- [S3 lifecycle expiration](https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-expire-general-considerations.html)
- [Litestream Go library considerations](https://litestream.io/guides/go-library/)


## Implementation notes

- R2 setup, lifecycle policy, restore, monitoring, and synthetic resource
  measurements are in [docs/backups.md](../backups.md).
- Uploads use single PutObject with a precomputed SHA-256 checksum and a
  5,000,000,000-byte compressed-file ceiling; multipart is not used.
- Local HTTP integration tests verify the S3 request, retries, checksum and
  restore. No live R2 credentials were supplied, so deployment verification
  remains an operator step.
