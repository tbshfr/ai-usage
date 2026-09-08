# Daily backups to Cloudflare R2

Backups are optional. When enabled, the application snapshots the entire stored
SQLite database with VACUUM INTO, validates it, gzip-compresses it, and uploads
one .sqlite.gz object. Successful daily backups give an approximately 24-hour
recovery window; failures or downtime extend it. There is no automatic restore.

## R2 setup

1. Create a private R2 bucket, preferably dedicated to these backups.
2. In **R2 → Account Details → API Tokens → Manage**, create an **Object Read &
   Write** token scoped to that bucket. Save its Access Key ID and Secret Access
   Key in your deployment's secret store.
3. Copy the bucket's S3 API endpoint. Use the jurisdiction-specific endpoint for
   a jurisdictional bucket (for example, ACCOUNT_ID.eu.r2.cloudflarestorage.com
   for EU). See [R2 authentication](https://developers.cloudflare.com/r2/api/tokens/).

Set these environment variables for the application process (replace placeholders):

~~~dotenv
AI_USAGE_BACKUP_S3_BUCKET=ai-usage-backups
AI_USAGE_BACKUP_S3_REGION=auto
AI_USAGE_BACKUP_S3_PREFIX=ai-usage/home/
AI_USAGE_BACKUP_S3_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
S3_ACCESS_KEY_ID=<R2_ACCESS_KEY_ID>
S3_SECRET_ACCESS_KEY=<R2_SECRET_ACCESS_KEY>
~~~

The region is **auto** for R2. Custom endpoints use path-style addressing
(/bucket/key); use the authenticated S3 endpoint, not a public bucket URL.
The SDK sends a precomputed SHA-256 checksum with each single PutObject.
See [R2's Go SDK example](https://developers.cloudflare.com/r2/examples/aws/aws-sdk-go/)
and [S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/).

Use a unique prefix ending in / for each database/instance. Flags override
environment variables. Without a bucket, backups are disabled and the AWS SDK is
not initialized; leave the other backup options unset too. Region falls back to
S3_REGION, then S3_DEFAULT_REGION, and must resolve when enabled. Credentials
come only from S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY, with optional
S3_SESSION_TOKEN. There are no application credential flags or automatic
shared-profile, SSO, workload-role, or instance-role discovery. Restart the app
after changing credentials. Missing credentials fail backup attempts without
stopping ingestion or the dashboard.

For Docker Compose, uncomment the backup environment block in
[compose-example.yaml](../compose-example.yaml) and fill in the commented entries
in [.env.example](../.env.example). A .env file alone does not pass variables
into the container. Temporary backups and scheduling state live beside the
database on /data; the example's small /tmp mount is not used for them.

The AWS CLI still requires its own AWS-prefixed variables. For the operator
commands below, define this shell function to map your S3 credentials for each
CLI invocation. Use the appropriate separate administrator or restore credentials
in the S3 variables before running those commands:

~~~sh
s3() {
  AWS_ACCESS_KEY_ID="$S3_ACCESS_KEY_ID" \
  AWS_SECRET_ACCESS_KEY="$S3_SECRET_ACCESS_KEY" \
  AWS_SESSION_TOKEN="${S3_SESSION_TOKEN:-}" \
    aws "$@"
}
~~~

## R2 retention lifecycle

Choose a positive retention period **X**; **7 days is a starting recommendation**.
In the bucket's **Settings → Object Lifecycle Rules → Add rule**, add an enabled
rule named ai-usage-home-expiration, prefix **ai-usage/home/**, deleting objects
after **7 days** (replace with X). Save it alongside existing rules. The equivalent
S3 lifecycle rule is:

~~~json
{
  "ID": "ai-usage-home-expiration",
  "Status": "Enabled",
  "Filter": { "Prefix": "ai-usage/home/" },
  "Expiration": { "Days": 7 }
}
~~~

Match the application prefix exactly. This is one rule, not a replacement bucket
configuration: merge it into the existing Rules array if using the S3 API.
Inspect the resulting policy in the dashboard or with administrator credentials:

~~~sh
s3 --endpoint-url "$AI_USAGE_BACKUP_S3_ENDPOINT" --region auto \
  s3api get-bucket-lifecycle-configuration --bucket "$AI_USAGE_BACKUP_S3_BUCKET"
~~~

Expiration is asynchronous, so it is not an exact storage cap.
Preserve R2's existing multipart-abort rule; this application uses no multipart
uploads. The app creates neither buckets nor lifecycle policies.
See [R2 object lifecycles](https://developers.cloudflare.com/r2/buckets/object-lifecycles/).

Approximate remote usage is X times the compressed snapshot size, plus expiration
delay and occasional duplicate uploads after a crash. Retention limits backup
history, not growth of the live database. **Age-based retention eventually deletes
every backup if uploads fail for longer than X days.** Monitor stale backups and
missing success logs; retention does not preserve a last known good object.

R2 does not support S3 bucket versioning. If using AWS S3 with versioning enabled
or suspended instead, current-object expiration alone leaves noncurrent data.
Add the following to the expiration rule:

~~~json
"NoncurrentVersionExpiration": { "NoncurrentDays": 7 }
~~~

Also add a separate rule for the same prefix with:

~~~json
"Expiration": { "ExpiredObjectDeleteMarker": true }
~~~

Choose the noncurrent period separately: its clock starts when a version becomes
noncurrent, so total storage can exceed X days. See
[AWS expiration semantics](https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-expire-general-considerations.html)
and [R2 compatibility](https://developers.cloudflare.com/r2/api/s3/api/).

## Permissions

The application only calls PutObject; it needs no listing, reading, deletion,
or lifecycle-administration permission. R2's standard persistent token permissions
are bucket-scoped and broader than this minimum; use a dedicated bucket. Use a
separate **Object Read only** token for restore and separate administrator
credentials for lifecycle management. Never give the running app an admin token.
See [R2 token scopes](https://developers.cloudflare.com/r2/api/tokens/).

For AWS IAM, the minimal upload statement is:

~~~json
{
  "Effect": "Allow",
  "Action": "s3:PutObject",
  "Resource": "arn:aws:s3:::ai-usage-backups/ai-usage/home/*"
}
~~~

Grant restore operators s3:GetObject on that prefix; optional s3:ListBucket
should have an s3:prefix condition. Lifecycle administrators need
s3:GetLifecycleConfiguration and s3:PutLifecycleConfiguration on the bucket.
AWS IAM policy JSON is not an R2 bucket policy.

## Scheduling, monitoring, and resources

On startup the worker runs immediately unless its last successful snapshot is
less than 24 hours old. After downtime it creates one current backup. Only one
worker runs per process; run one application process per database. A private
directory named `<database>.backups-<destination-hash>` contains success.json,
recording snapshot time, completion time, and key via atomic replacement.
Keep this directory with the deployment; deleting its state causes an immediate
backup next startup. Changing database path or destination starts a new schedule.

Each attempt has a 30-minute deadline. The SDK retries transient uploads up to
three times using the same local file and key. Failed attempts retry with
exponential backoff and jitter (initially 30–60 seconds, capped at 30–60 minutes).
Failures never advance the saved schedule. A crash between upload and state
persistence may leave an extra object. Temporary artifacts are removed after
attempts and stale attempt artifacts are cleaned on startup.

At the default info log level, collect JSON messages **backups enabled**,
**backup last success**, **backup succeeded**, and **backup failed**. Outcomes
include elapsed time, compressed bytes, and snapshot timestamps; failure **stage**
identifies snapshot, compression, upload, or state problems without exposing SDK
errors or signed URLs. **backup stale** is emitted after a failed attempt without
a recent success, at most hourly. Alert on this warning and on absence of a
successful snapshot for more than 24 hours (allow a small completion margin).
Monitor process availability too: a stopped process cannot warn. An upload-stage
failure calls for checking credentials, endpoint, network access, and provider
status. /health and /ready remain independent of remote backup availability.

Allow free disk space beside the database for one standalone snapshot **plus**
its gzip file and WAL growth during the read. Compression and upload stream data
with bounded memory. This version rejects compressed files over 5,000,000,000
bytes, below [R2's single-upload limit](https://developers.cloudflare.com/r2/platform/limits/).
Snapshotting consumes CPU and I/O and can increase ingestion latency.
The schema uses explicit application keys and never relies on implicit ROWIDs
remaining stable under VACUUM. Backup files contain all stored metadata, including
conversation IDs and repository metadata; gzip is not encryption. Enabling
backups sends that database off the machine.

## Restore

Use the same application version recorded in object metadata, or a compatible
newer version. The following Linux example needs AWS CLI, gzip, OpenSSL, and the
SQLite CLI on the operator's machine; they are not runtime app dependencies.

1. Stop the application completely (for example, docker compose stop ai-usage).
   Use separate restore credentials and select an exact .sqlite.gz object key
   from R2 or the success log.
2. Download and validate in a new private staging directory. In a shell with the
   R2 bucket/endpoint variables above and restore credentials:

~~~sh
set -eu
umask 077
BACKUP_KEY='ai-usage/home/<timestamp>-<suffix>.sqlite.gz'
RESTORE_DIR=$(mktemp -d)
s3 --endpoint-url "$AI_USAGE_BACKUP_S3_ENDPOINT" --region auto \
  s3api get-object --bucket "$AI_USAGE_BACKUP_S3_BUCKET" --key "$BACKUP_KEY" \
  "$RESTORE_DIR/backup.sqlite.gz" > "$RESTORE_DIR/object-metadata.json"
EXPECTED_SHA256=$(s3 --endpoint-url "$AI_USAGE_BACKUP_S3_ENDPOINT" --region auto \
  s3api head-object --bucket "$AI_USAGE_BACKUP_S3_BUCKET" --key "$BACKUP_KEY" \
  --query 'Metadata.sha256' --output text)
ACTUAL_SHA256=$(openssl dgst -sha256 -binary "$RESTORE_DIR/backup.sqlite.gz" | openssl base64 -A)
test "$EXPECTED_SHA256" = "$ACTUAL_SHA256"
gzip -t "$RESTORE_DIR/backup.sqlite.gz"
gzip -dc "$RESTORE_DIR/backup.sqlite.gz" > "$RESTORE_DIR/usage.db"
test "$(sqlite3 -readonly "$RESTORE_DIR/usage.db" 'PRAGMA integrity_check;')" = ok
sqlite3 -readonly "$RESTORE_DIR/usage.db" \
  'SELECT count(*), sum(input_tokens), sum(output_tokens), sum(cost) FROM generations;'
~~~

3. Preserve the original database **and any WAL/SHM companions together**, while
   the app remains stopped. Adjust DB and service ownership for your deployment:

~~~sh
DB="$PWD/data/usage.db"
PRESERVED_DIR=$(mktemp -d "$(dirname "$DB")/before-restore.XXXXXX")
for FILE in "$DB" "$DB-wal" "$DB-shm"; do
  if [ -e "$FILE" ]; then mv -- "$FILE" "$PRESERVED_DIR/"; fi
done
install -m 600 "$RESTORE_DIR/usage.db" "$DB"
# Run with suitable privileges; 65532:65532 is the example container's user.
chown 65532:65532 "$DB"
~~~

Do not proceed after any failed command. If installation fails, keep the app
stopped and recover the original set from PRESERVED_DIR. The restored file must
have no stale usage.db-wal or usage.db-shm beside it.

4. Restart the application. Verify logs, representative records, and dashboard
   token/cost totals against the staged snapshot. Daily ingestion counters can
   lag generation records by the one-minute counter-save interval. Keep the
   preserved originals until validation succeeds. Restoring never merges newer
   live records automatically.

## Deployment smoke test

Use a disposable R2 bucket or a separate test prefix and an isolated application
data directory. Ingest a known fixture, wait for **backup succeeded**, and confirm
the object's snapshot/version/checksum metadata. Inspect lifecycle rules with
administrator credentials and confirm the exact test prefix/age without changing
production retention.

Download the object and perform the staging validation above. Start a second
application on a separate directory and loopback port, with backups and OTLP
listeners unset, using the restored database. Check its records and dashboard
totals against the snapshot. Restart the test backup application before 24 hours
and confirm it logs its saved success without uploading another object.

Automated tests use a local HTTP S3 server; an actual R2 smoke test requires your
bucket and credentials and must be completed in your deployment.

## Local measurements

Run the reproducible synthetic workload with:

~~~sh
go test ./internal/backup -run '^$' -bench BenchmarkBackup -benchtime=1x -count=1
~~~

This reports snapshot/validation time, compressed bytes, and storage ingestion
batch latency with and without backup activity. Synthetic results are not a
capacity guarantee; measure your real database and deployment resource limits
before relying on the daily recovery window.


Measured on Linux amd64, Intel Core i7-1260P, with 100,000 synthetic generation
records across three sources, random IDs and conversation IDs, ten models and
fifty repositories (one run, local filesystem):

| Measurement | Result |
|---|---:|
| Snapshot plus integrity validation | 0.828 s |
| Snapshot, validation and gzip under concurrent writes | 1.352 s |
| Compressed snapshot | 14,127,243 bytes (13.47 MiB) |
| Ten-record ingestion batch p95, baseline | 3.180 ms |
| Ten-record ingestion batch p95, during backup | 5.561 ms |

Latency measures the application's storage ingestion function, excluding OTLP
parsing and network time. The writer pauses 1 ms between batches. This was not
run under the example container's 1-CPU/256-MiB limits and includes no R2 network
upload; allow for slower disks, larger databases, and network transfer time.
